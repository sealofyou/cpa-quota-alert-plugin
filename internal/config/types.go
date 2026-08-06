package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
)

const (
	Window5h = "5h"
	Window7d = "7d"
)

const DefaultQuotaURL = "https://chatgpt.com/backend-api/wham/usage"

type Getenv func(string) string

type PlanRule struct {
	Name    string
	Aliases []string
	Window  string
	Weight  float64
}

type PlanSet map[string]struct{}

func (s PlanSet) Contains(plan string) bool {
	_, ok := s[NormalizePlan(plan)]
	return ok
}

type SMTPConfig struct {
	Enabled         bool
	Host            string
	Port            int
	UsernameEnv     string
	PasswordEnv     string
	RecipientsEnv   string
	FromEnv         string
	Recipients      []string
	RecipientEnv    string
	NonSecretLabels map[string]string
}

type WebhookConfig struct {
	Enabled            bool
	URLenv             string
	HeaderEnv          string
	URLRuntimeEnv      string
	AuthHeaderEnv      string
	NonSecretMetadata  map[string]string
	ExtraHeaderNameEnv string
}

type Config struct {
	DryRun                bool
	Concurrency           int
	RequestTimeoutSeconds int
	RetryAttempts         int
	StaleAfterSeconds     int
	LowThreshold          float64
	RecoveryThreshold     float64
	ReminderSeconds       int
	FailureAlertCount     int
	StatePath             string
	PlanRules             map[string]PlanRule
	Aliases               map[string]string
	IgnoredPlans          PlanSet
	TerminalErrorCodes    PlanSet
	QuotaURL              string
	AllowedQuotaHosts     PlanSet
	SMTP                  SMTPConfig
	Webhook               WebhookConfig
}

func Parse(raw map[string]any, getenv Getenv) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if raw == nil {
		raw = map[string]any{}
	}
	if secretPath := findPlainSecret(raw, nil); secretPath != "" {
		return Config{}, fmt.Errorf("config field %q must reference an environment variable name, not a plaintext secret", secretPath)
	}
	cfg := Config{
		Concurrency:           4,
		RequestTimeoutSeconds: 45,
		RetryAttempts:         2,
		StaleAfterSeconds:     900,
		LowThreshold:          1.5,
		RecoveryThreshold:     1.6,
		ReminderSeconds:       86400,
		FailureAlertCount:     3,
		QuotaURL:              DefaultQuotaURL,
		AllowedQuotaHosts:     PlanSet{"chatgpt.com": {}},
		IgnoredPlans:          PlanSet{"free": {}},
		TerminalErrorCodes:    PlanSet{},
	}

	cfg.DryRun = boolValue(raw, "dry_run", cfg.DryRun)
	cfg.Concurrency = intValue(raw, "concurrency", cfg.Concurrency)
	cfg.RequestTimeoutSeconds = intValue(raw, "request_timeout_seconds", cfg.RequestTimeoutSeconds)
	cfg.RetryAttempts = intValue(raw, "retry_attempts", cfg.RetryAttempts)
	cfg.StaleAfterSeconds = intValue(raw, "stale_after_seconds", cfg.StaleAfterSeconds)
	cfg.LowThreshold = floatValue(raw, "low_threshold", cfg.LowThreshold)
	cfg.RecoveryThreshold = floatValue(raw, "recovery_threshold", cfg.RecoveryThreshold)
	cfg.ReminderSeconds = intValue(raw, "reminder_seconds", cfg.ReminderSeconds)
	cfg.FailureAlertCount = intValue(raw, "failure_alert_count", cfg.FailureAlertCount)
	cfg.StatePath = stringValue(raw, "state_path", cfg.StatePath)
	cfg.QuotaURL = stringValue(raw, "quota_url", cfg.QuotaURL)

	if cfg.RecoveryThreshold <= cfg.LowThreshold {
		return Config{}, errors.New("recovery_threshold must be greater than low_threshold")
	}
	if hosts, ok := raw["allowed_quota_hosts"]; ok {
		cfg.AllowedQuotaHosts = parseSet(hosts)
	}
	if ignored, ok := raw["ignored_plans"]; ok {
		cfg.IgnoredPlans = parsePlanSet(ignored)
	}
	if terminal, ok := raw["terminal_error_codes"]; ok {
		cfg.TerminalErrorCodes = parseSet(terminal)
	}
	if smtpRaw, ok := objectValue(raw["smtp"]); ok {
		cfg.SMTP = parseSMTP(smtpRaw)
	}
	if webhookRaw, ok := objectValue(raw["webhook"]); ok {
		cfg.Webhook = parseWebhook(webhookRaw)
	}
	if err := validateNotificationEnv(cfg, getenv); err != nil {
		return Config{}, err
	}

	parsedURL, err := url.Parse(cfg.QuotaURL)
	if err != nil || !parsedURL.IsAbs() || parsedURL.Hostname() == "" {
		return Config{}, fmt.Errorf("quota_url must be absolute: %q", cfg.QuotaURL)
	}
	if !cfg.AllowedQuotaHosts.Contains(parsedURL.Hostname()) {
		return Config{}, fmt.Errorf("quota_url host %q is not in allowed_quota_hosts", parsedURL.Hostname())
	}

	rules, aliases, err := parseRules(raw["plan_rules"], cfg.IgnoredPlans)
	if err != nil {
		return Config{}, err
	}
	cfg.PlanRules = rules
	cfg.Aliases = aliases
	return cfg, nil
}

func validateNotificationEnv(cfg Config, getenv Getenv) error {
	if cfg.SMTP.Enabled {
		if err := requireEnv(getenv, "smtp.password_env", cfg.SMTP.PasswordEnv); err != nil {
			return err
		}
		if err := requireEnv(getenv, "smtp.recipients_env", cfg.SMTP.RecipientsEnv); err != nil {
			return err
		}
	}
	if cfg.Webhook.Enabled {
		if err := requireEnv(getenv, "webhook.url_env", cfg.Webhook.URLenv); err != nil {
			return err
		}
		if err := requireEnv(getenv, "webhook.auth_header_env", cfg.Webhook.AuthHeaderEnv); err != nil {
			return err
		}
	}
	return nil
}

func requireEnv(getenv Getenv, label, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%s must name an environment variable when channel is enabled", label)
	}
	if strings.TrimSpace(getenv(name)) == "" {
		return fmt.Errorf("%s environment variable %q is required and must be non-empty", label, name)
	}
	return nil
}
func parseRules(raw any, ignored PlanSet) (map[string]PlanRule, map[string]string, error) {
	var candidates []any
	if raw == nil {
		candidates = []any{
			map[string]any{"name": "plus", "aliases": []any{"plus"}, "window": Window7d, "weight": float64(1)},
			map[string]any{"name": "team", "aliases": []any{"team"}, "window": Window7d, "weight": float64(1)},
		}
	} else {
		var ok bool
		candidates, ok = arrayValue(raw)
		if !ok {
			return nil, nil, errors.New("plan_rules must be an array")
		}
	}
	rules := map[string]PlanRule{}
	aliases := map[string]string{}
	for i, candidate := range candidates {
		obj, ok := objectValue(candidate)
		if !ok {
			return nil, nil, fmt.Errorf("plan_rules[%d] must be an object", i)
		}
		name := NormalizePlan(stringValue(obj, "name", stringValue(obj, "canonical_name", "")))
		if name == "" {
			return nil, nil, fmt.Errorf("plan_rules[%d].name is required", i)
		}
		if ignored.Contains(name) {
			return nil, nil, fmt.Errorf("plan rule %q conflicts with ignored_plans", name)
		}
		window := stringValue(obj, "window", "")
		if window != Window5h && window != Window7d {
			return nil, nil, fmt.Errorf("plan rule %q window must be 5h or 7d", name)
		}
		weight := floatValue(obj, "weight", 0)
		if weight <= 0 {
			return nil, nil, fmt.Errorf("plan rule %q weight must be greater than 0", name)
		}
		rule := PlanRule{Name: name, Window: window, Weight: weight}
		if rawAliases, ok := obj["aliases"]; ok {
			aliasValues, ok := arrayValue(rawAliases)
			if !ok {
				return nil, nil, fmt.Errorf("plan rule %q aliases must be an array", name)
			}
			for _, aliasRaw := range aliasValues {
				alias := NormalizePlan(asString(aliasRaw))
				if alias != "" {
					rule.Aliases = append(rule.Aliases, alias)
				}
			}
		}
		if !slices.Contains(rule.Aliases, name) {
			rule.Aliases = append(rule.Aliases, name)
		}
		if _, exists := rules[name]; exists {
			return nil, nil, fmt.Errorf("duplicate plan rule %q", name)
		}
		for _, alias := range rule.Aliases {
			if ignored.Contains(alias) {
				return nil, nil, fmt.Errorf("plan alias %q conflicts with ignored_plans", alias)
			}
			if previous, exists := aliases[alias]; exists && previous != name {
				return nil, nil, fmt.Errorf("plan alias %q conflicts between %q and %q", alias, previous, name)
			}
			aliases[alias] = name
		}
		rules[name] = rule
	}
	return rules, aliases, nil
}

func NormalizePlan(value string) string {
	lower := strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(" ", "", "_", "", "-", "")
	return replacer.Replace(lower)
}

func parseSMTP(raw map[string]any) SMTPConfig {
	return SMTPConfig{
		Enabled:         boolValue(raw, "enabled", false),
		Host:            stringValue(raw, "host", ""),
		Port:            intValue(raw, "port", 0),
		UsernameEnv:     firstString(raw, "username_env", "smtp_username_env"),
		PasswordEnv:     firstString(raw, "password_env", "smtp_password_env"),
		RecipientsEnv:   firstString(raw, "recipients_env", "smtp_recipients_env"),
		FromEnv:         firstString(raw, "from_env", "smtp_from_env"),
		RecipientEnv:    firstString(raw, "recipient_env"),
		Recipients:      stringSlice(raw["recipients"]),
		NonSecretLabels: stringMap(raw["metadata"]),
	}
}

func parseWebhook(raw map[string]any) WebhookConfig {
	return WebhookConfig{
		Enabled:            boolValue(raw, "enabled", false),
		URLenv:             firstString(raw, "url_env", "webhook_url_env"),
		HeaderEnv:          firstString(raw, "header_env", "webhook_header_env"),
		URLRuntimeEnv:      firstString(raw, "runtime_url_env"),
		AuthHeaderEnv:      firstString(raw, "auth_header_env"),
		NonSecretMetadata:  stringMap(raw["metadata"]),
		ExtraHeaderNameEnv: firstString(raw, "extra_header_name_env"),
	}
}

func findPlainSecret(raw any, path []string) string {
	switch v := raw.(type) {
	case map[string]any:
		for k, value := range v {
			lower := strings.ToLower(k)
			next := append(append([]string{}, path...), k)
			if isForbiddenPlainSecretKey(lower) {
				return strings.Join(next, ".")
			}
			if found := findPlainSecret(value, next); found != "" {
				return found
			}
		}
	case []any:
		for i, value := range v {
			if found := findPlainSecret(value, append(path, strconv.Itoa(i))); found != "" {
				return found
			}
		}
	}
	return ""
}

func isForbiddenPlainSecretKey(key string) bool {
	for _, forbidden := range []string{"smtp_password", "webhook_url", "auth_header"} {
		if key == forbidden || (strings.Contains(key, forbidden) && !strings.HasSuffix(key, "_env")) {
			return true
		}
	}
	return false
}

func parseSet(raw any) PlanSet {
	out := PlanSet{}
	for _, item := range stringSlice(raw) {
		normalized := NormalizePlan(item)
		if normalized != "" {
			out[normalized] = struct{}{}
		}
	}
	return out
}

func parsePlanSet(raw any) PlanSet { return parseSet(raw) }

func stringSlice(raw any) []string {
	values, ok := arrayValue(raw)
	if !ok {
		if s := asString(raw); s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if s := asString(value); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func stringMap(raw any) map[string]string {
	obj, ok := objectValue(raw)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for key, value := range obj {
		out[key] = asString(value)
	}
	return out
}

func boolValue(raw map[string]any, key string, fallback bool) bool {
	value, ok := raw[key]
	if !ok {
		return fallback
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		parsed, err := strconv.ParseBool(v)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func intValue(raw map[string]any, key string, fallback int) int {
	value, ok := raw[key]
	if !ok {
		return fallback
	}
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		parsed, err := strconv.Atoi(v)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func floatValue(raw map[string]any, key string, fallback float64) float64 {
	value, ok := raw[key]
	if !ok {
		return fallback
	}
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		parsed, err := strconv.ParseFloat(v, 64)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func stringValue(raw map[string]any, key, fallback string) string {
	if value, ok := raw[key]; ok {
		if s := asString(value); s != "" {
			return s
		}
	}
	return fallback
}

func firstString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(raw, key, ""); value != "" {
			return value
		}
	}
	return ""
}

func asString(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}

func objectValue(raw any) (map[string]any, bool) {
	value, ok := raw.(map[string]any)
	return value, ok
}

func arrayValue(raw any) ([]any, bool) {
	switch v := raw.(type) {
	case []any:
		return v, true
	case []map[string]any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = v[i]
		}
		return out, true
	case []string:
		out := make([]any, len(v))
		for i := range v {
			out[i] = v[i]
		}
		return out, true
	default:
		return nil, false
	}
}
