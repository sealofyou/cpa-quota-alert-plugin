package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
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

const (
	DefaultQuotaURL  = "https://chatgpt.com/backend-api/wham/usage"
	DefaultStatePath = "cpa-quota-alert-state.json"
)

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

type HostSet map[string]struct{}

func (s HostSet) Contains(host string) bool {
	_, ok := s[NormalizeHost(host)]
	return ok
}

type StringSet map[string]struct{}

func (s StringSet) Contains(value string) bool {
	_, ok := s[strings.TrimSpace(value)]
	return ok
}

type SMTPConfig struct {
	Enabled        bool
	Host           string
	Port           int
	TLSMode        string
	TimeoutSeconds int
	UsernameEnv    string
	PasswordEnv    string
	RecipientsEnv  string
	FromEnv        string
	FromName       string

	// Kept for clone compatibility with adjacent packages; config parsing no longer accepts metadata here.
	NonSecretLabels map[string]string
}

type WebhookConfig struct {
	Enabled        bool
	Method         string
	TimeoutSeconds int
	URLEnv         string
	AuthHeaderEnv  string

	// Kept for clone compatibility with adjacent packages; config parsing no longer accepts metadata here.
	NonSecretMetadata map[string]string
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
	TerminalErrorCodes    StringSet
	QuotaURL              string
	AllowedQuotaHosts     HostSet
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
		StatePath:             DefaultStatePath,
		QuotaURL:              DefaultQuotaURL,
		AllowedQuotaHosts:     HostSet{"chatgpt.com": {}},
		IgnoredPlans:          PlanSet{"free": {}},
		TerminalErrorCodes:    StringSet{},
	}

	var err error
	if cfg.DryRun, err = readBool(raw, "dry_run", cfg.DryRun); err != nil {
		return Config{}, err
	}
	if cfg.Concurrency, err = readInt(raw, "concurrency", cfg.Concurrency); err != nil {
		return Config{}, err
	}
	if cfg.RequestTimeoutSeconds, err = readInt(raw, "request_timeout_seconds", cfg.RequestTimeoutSeconds); err != nil {
		return Config{}, err
	}
	if cfg.RetryAttempts, err = readInt(raw, "retry_attempts", cfg.RetryAttempts); err != nil {
		return Config{}, err
	}
	if cfg.StaleAfterSeconds, err = readInt(raw, "stale_after_seconds", cfg.StaleAfterSeconds); err != nil {
		return Config{}, err
	}
	if cfg.LowThreshold, err = readFloat(raw, "low_threshold", cfg.LowThreshold); err != nil {
		return Config{}, err
	}
	if cfg.RecoveryThreshold, err = readFloat(raw, "recovery_threshold", cfg.RecoveryThreshold); err != nil {
		return Config{}, err
	}
	if cfg.ReminderSeconds, err = readInt(raw, "reminder_seconds", cfg.ReminderSeconds); err != nil {
		return Config{}, err
	}
	if cfg.FailureAlertCount, err = readInt(raw, "failure_alert_count", cfg.FailureAlertCount); err != nil {
		return Config{}, err
	}
	if cfg.StatePath, err = readString(raw, "state_path", cfg.StatePath); err != nil {
		return Config{}, err
	}
	if cfg.QuotaURL, err = readString(raw, "quota_url", cfg.QuotaURL); err != nil {
		return Config{}, err
	}
	if err := validateRanges(cfg); err != nil {
		return Config{}, err
	}

	if hosts, ok := raw["allowed_quota_hosts"]; ok {
		parsed, err := parseHostSet(hosts, "allowed_quota_hosts")
		if err != nil {
			return Config{}, err
		}
		cfg.AllowedQuotaHosts = parsed
	}
	if ignored, ok := raw["ignored_plans"]; ok {
		parsed, err := parsePlanSet(ignored, "ignored_plans")
		if err != nil {
			return Config{}, err
		}
		cfg.IgnoredPlans = parsed
	}
	if terminal, ok := raw["terminal_error_codes"]; ok {
		parsed, err := parseStringSet(terminal, "terminal_error_codes")
		if err != nil {
			return Config{}, err
		}
		cfg.TerminalErrorCodes = parsed
	}
	if smtpValue, exists := raw["smtp"]; exists {
		smtpRaw, ok := objectValue(smtpValue)
		if !ok {
			return Config{}, errors.New("smtp must be an object")
		}
		cfg.SMTP, err = parseSMTP(smtpRaw)
		if err != nil {
			return Config{}, err
		}
	}
	if webhookValue, exists := raw["webhook"]; exists {
		webhookRaw, ok := objectValue(webhookValue)
		if !ok {
			return Config{}, errors.New("webhook must be an object")
		}
		cfg.Webhook, err = parseWebhook(webhookRaw)
		if err != nil {
			return Config{}, err
		}
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

func validateRanges(cfg Config) error {
	positive := map[string]int{
		"concurrency":             cfg.Concurrency,
		"request_timeout_seconds": cfg.RequestTimeoutSeconds,
		"stale_after_seconds":     cfg.StaleAfterSeconds,
		"reminder_seconds":        cfg.ReminderSeconds,
		"failure_alert_count":     cfg.FailureAlertCount,
	}
	for field, value := range positive {
		if value <= 0 {
			return fmt.Errorf("%s must be greater than 0", field)
		}
	}
	if cfg.RetryAttempts < 0 {
		return errors.New("retry_attempts must be greater than or equal to 0")
	}
	if strings.TrimSpace(cfg.StatePath) == "" {
		return errors.New("state_path must be non-empty")
	}
	if !isFinite(cfg.LowThreshold) || cfg.LowThreshold <= 0 {
		return errors.New("low_threshold must be finite and greater than 0")
	}
	if !isFinite(cfg.RecoveryThreshold) || cfg.RecoveryThreshold <= cfg.LowThreshold {
		return errors.New("recovery_threshold must be finite and greater than low_threshold")
	}
	return nil
}

func validateNotificationEnv(cfg Config, getenv Getenv) error {
	type envRequirement struct {
		label string
		name  string
	}
	var requirements []envRequirement
	if cfg.SMTP.Enabled {
		requirements = append(requirements,
			envRequirement{label: "smtp.username_env", name: cfg.SMTP.UsernameEnv},
			envRequirement{label: "smtp.password_env", name: cfg.SMTP.PasswordEnv},
			envRequirement{label: "smtp.recipients_env", name: cfg.SMTP.RecipientsEnv},
			envRequirement{label: "smtp.from_env", name: cfg.SMTP.FromEnv},
		)
	}
	if cfg.Webhook.Enabled {
		requirements = append(requirements,
			envRequirement{label: "webhook.url_env", name: cfg.Webhook.URLEnv},
			envRequirement{label: "webhook.auth_header_env", name: cfg.Webhook.AuthHeaderEnv},
		)
	}
	for _, item := range requirements {
		if err := validateEnvName(item.label, item.name); err != nil {
			return err
		}
	}
	for _, item := range requirements {
		if err := requireEnv(getenv, item.label, item.name); err != nil {
			return err
		}
	}
	return nil
}

func requireEnv(getenv Getenv, label, name string) error {
	if err := validateEnvName(label, name); err != nil {
		return err
	}
	if strings.TrimSpace(getenv(name)) == "" {
		return fmt.Errorf("%s environment variable %q is required and must be non-empty", label, name)
	}
	return nil
}

func validateEnvName(label, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%s must name an environment variable when channel is enabled", label)
	}
	if !isEnvName(name) {
		return fmt.Errorf("%s must name a valid environment variable", label)
	}
	return nil
}

func isEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r > 127 {
			return false
		}
		if i == 0 {
			if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
				return false
			}
			continue
		}
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
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
		name, err := readString(obj, "name", "")
		if err != nil {
			return nil, nil, err
		}
		if name == "" {
			name, err = readString(obj, "canonical_name", "")
			if err != nil {
				return nil, nil, err
			}
		}
		name = NormalizePlan(name)
		if name == "" {
			return nil, nil, fmt.Errorf("plan_rules[%d].name is required", i)
		}
		if ignored.Contains(name) {
			return nil, nil, fmt.Errorf("plan rule %q conflicts with ignored_plans", name)
		}
		window, err := readString(obj, "window", "")
		if err != nil {
			return nil, nil, err
		}
		if window != Window5h && window != Window7d {
			return nil, nil, fmt.Errorf("plan rule %q window must be 5h or 7d", name)
		}
		weight, err := readFloat(obj, "weight", 0)
		if err != nil {
			return nil, nil, err
		}
		if weight <= 0 {
			return nil, nil, fmt.Errorf("plan rule %q weight must be greater than 0", name)
		}
		rule := PlanRule{Name: name, Window: window, Weight: weight}
		if rawAliases, ok := obj["aliases"]; ok {
			aliasValues, err := parseStringList(rawAliases, fmt.Sprintf("plan_rules[%d].aliases", i))
			if err != nil {
				return nil, nil, err
			}
			for _, alias := range aliasValues {
				alias = NormalizePlan(alias)
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

func NormalizeHost(value string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(value)), ".")
}

func parseSMTP(raw map[string]any) (SMTPConfig, error) {
	allowed := map[string]struct{}{
		"enabled": {}, "host": {}, "port": {}, "tls_mode": {}, "timeout_seconds": {},
		"username_env": {}, "password_env": {}, "recipients_env": {}, "from_env": {}, "from_name": {},
	}
	if err := rejectUnknownFields(raw, "smtp", allowed); err != nil {
		return SMTPConfig{}, err
	}
	var err error
	cfg := SMTPConfig{TLSMode: "starttls", TimeoutSeconds: 10}
	if cfg.Enabled, err = readBool(raw, "enabled", false); err != nil {
		return SMTPConfig{}, err
	}
	if cfg.Host, err = readString(raw, "host", ""); err != nil {
		return SMTPConfig{}, err
	}
	if cfg.Port, err = readInt(raw, "port", 0); err != nil {
		return SMTPConfig{}, err
	}
	if cfg.TLSMode, err = readString(raw, "tls_mode", cfg.TLSMode); err != nil {
		return SMTPConfig{}, err
	}
	if cfg.TimeoutSeconds, err = readInt(raw, "timeout_seconds", cfg.TimeoutSeconds); err != nil {
		return SMTPConfig{}, err
	}
	if cfg.UsernameEnv, err = readString(raw, "username_env", ""); err != nil {
		return SMTPConfig{}, err
	}
	if cfg.PasswordEnv, err = readString(raw, "password_env", ""); err != nil {
		return SMTPConfig{}, err
	}
	if cfg.RecipientsEnv, err = readString(raw, "recipients_env", ""); err != nil {
		return SMTPConfig{}, err
	}
	if cfg.FromEnv, err = readString(raw, "from_env", ""); err != nil {
		return SMTPConfig{}, err
	}
	if cfg.FromName, err = readString(raw, "from_name", ""); err != nil {
		return SMTPConfig{}, err
	}
	if cfg.Enabled {
		if strings.TrimSpace(cfg.Host) == "" {
			return SMTPConfig{}, errors.New("smtp.host is required when smtp is enabled")
		}
		if cfg.Port <= 0 || cfg.Port > 65535 {
			return SMTPConfig{}, errors.New("smtp.port must be between 1 and 65535")
		}
		if cfg.TLSMode != "implicit_tls" && cfg.TLSMode != "starttls" {
			return SMTPConfig{}, errors.New("smtp.tls_mode must be implicit_tls or starttls")
		}
		if cfg.TimeoutSeconds <= 0 {
			return SMTPConfig{}, errors.New("smtp.timeout_seconds must be greater than 0")
		}
	}
	return cfg, nil
}

func parseWebhook(raw map[string]any) (WebhookConfig, error) {
	allowed := map[string]struct{}{
		"enabled": {}, "method": {}, "timeout_seconds": {}, "url_env": {}, "auth_header_env": {},
	}
	if err := rejectUnknownFields(raw, "webhook", allowed); err != nil {
		return WebhookConfig{}, err
	}
	var err error
	cfg := WebhookConfig{Method: "POST", TimeoutSeconds: 10}
	if cfg.Enabled, err = readBool(raw, "enabled", false); err != nil {
		return WebhookConfig{}, err
	}
	if cfg.Method, err = readString(raw, "method", cfg.Method); err != nil {
		return WebhookConfig{}, err
	}
	cfg.Method = strings.ToUpper(cfg.Method)
	if cfg.TimeoutSeconds, err = readInt(raw, "timeout_seconds", cfg.TimeoutSeconds); err != nil {
		return WebhookConfig{}, err
	}
	if cfg.URLEnv, err = readString(raw, "url_env", ""); err != nil {
		return WebhookConfig{}, err
	}
	if cfg.AuthHeaderEnv, err = readString(raw, "auth_header_env", ""); err != nil {
		return WebhookConfig{}, err
	}
	if cfg.Enabled {
		if cfg.Method != "POST" && cfg.Method != "PUT" {
			return WebhookConfig{}, errors.New("webhook.method must be POST or PUT")
		}
		if cfg.TimeoutSeconds <= 0 {
			return WebhookConfig{}, errors.New("webhook.timeout_seconds must be greater than 0")
		}
	}
	return cfg, nil
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
	if strings.HasSuffix(key, "_env") || key == "from_name" {
		return false
	}
	for _, forbidden := range []string{"password", "username", "recipient", "recipients", "from"} {
		if key == forbidden || key == "smtp_"+forbidden {
			return true
		}
	}
	for _, forbidden := range []string{"url", "auth_header", "header"} {
		if key == forbidden || key == "webhook_"+forbidden {
			return true
		}
	}
	return false
}

func rejectUnknownFields(raw map[string]any, prefix string, allowed map[string]struct{}) error {
	for key := range raw {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("%s.%s is not allowed", prefix, key)
		}
	}
	return nil
}

func parsePlanSet(raw any, field string) (PlanSet, error) {
	items, err := parseStringList(raw, field)
	if err != nil {
		return nil, err
	}
	out := PlanSet{}
	for _, item := range items {
		if normalized := NormalizePlan(item); normalized != "" {
			out[normalized] = struct{}{}
		}
	}
	return out, nil
}

func parseHostSet(raw any, field string) (HostSet, error) {
	items, err := parseStringList(raw, field)
	if err != nil {
		return nil, err
	}
	out := HostSet{}
	for _, item := range items {
		if normalized := NormalizeHost(item); normalized != "" {
			out[normalized] = struct{}{}
		}
	}
	return out, nil
}

func parseStringSet(raw any, field string) (StringSet, error) {
	items, err := parseStringList(raw, field)
	if err != nil {
		return nil, err
	}
	out := StringSet{}
	for _, item := range items {
		if normalized := strings.TrimSpace(item); normalized != "" {
			out[normalized] = struct{}{}
		}
	}
	return out, nil
}

func parseStringList(raw any, field string) ([]string, error) {
	switch values := raw.(type) {
	case []string:
		out := make([]string, 0, len(values))
		for i, value := range values {
			if strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("%s[%d] must be a non-empty string", field, i)
			}
			out = append(out, strings.TrimSpace(value))
		}
		return out, nil
	case []any:
		out := make([]string, 0, len(values))
		for i, value := range values {
			text, ok := value.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return nil, fmt.Errorf("%s[%d] must be a non-empty string", field, i)
			}
			out = append(out, strings.TrimSpace(text))
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%s must be an array of strings", field)
	}
}
func stringMap(raw any) map[string]string {
	obj, ok := objectValue(raw)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for key, value := range obj {
		if s, ok := value.(string); ok {
			out[key] = strings.TrimSpace(s)
		}
	}
	return out
}

func readBool(raw map[string]any, key string, fallback bool) (bool, error) {
	value, ok := raw[key]
	if !ok {
		return fallback, nil
	}
	b, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return b, nil
}

func readInt(raw map[string]any, key string, fallback int) (int, error) {
	value, ok := raw[key]
	if !ok {
		return fallback, nil
	}
	parsed, err := strictInt(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return parsed, nil
}

func readFloat(raw map[string]any, key string, fallback float64) (float64, error) {
	value, ok := raw[key]
	if !ok {
		return fallback, nil
	}
	parsed, err := strictFloat(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a finite number: %w", key, err)
	}
	return parsed, nil
}

func readString(raw map[string]any, key, fallback string) (string, error) {
	value, ok := raw[key]
	if !ok {
		return fallback, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return strings.TrimSpace(text), nil
}

func firstString(raw map[string]any, keys ...string) (string, error) {
	for _, key := range keys {
		if _, ok := raw[key]; ok {
			return readString(raw, key, "")
		}
	}
	return "", nil
}

func strictInt(value any) (int, error) {
	var n int64
	switch v := value.(type) {
	case int:
		return v, nil
	case int8:
		return int(v), nil
	case int16:
		return int(v), nil
	case int32:
		return int(v), nil
	case int64:
		n = v
	case uint:
		if uint64(v) > uint64(maxInt()) {
			return 0, errors.New("overflows int")
		}
		return int(v), nil
	case uint8:
		return int(v), nil
	case uint16:
		return int(v), nil
	case uint32:
		if uint64(v) > uint64(maxInt()) {
			return 0, errors.New("overflows int")
		}
		return int(v), nil
	case uint64:
		if v > uint64(maxInt()) {
			return 0, errors.New("overflows int")
		}
		return int(v), nil
	case float64:
		if !isFinite(v) || math.Trunc(v) != v {
			return 0, errors.New("must be finite and integral")
		}
		if v < float64(minInt()) || v > float64(maxInt()) {
			return 0, errors.New("overflows int")
		}
		return int(v), nil
	case float32:
		f := float64(v)
		if !isFinite(f) || math.Trunc(f) != f {
			return 0, errors.New("must be finite and integral")
		}
		if f < float64(minInt()) || f > float64(maxInt()) {
			return 0, errors.New("overflows int")
		}
		return int(f), nil
	case json.Number:
		parsed, err := strconv.ParseFloat(v.String(), 64)
		if err != nil || !isFinite(parsed) || math.Trunc(parsed) != parsed {
			return 0, errors.New("must be integral")
		}
		if parsed < float64(minInt()) || parsed > float64(maxInt()) {
			return 0, errors.New("overflows int")
		}
		return int(parsed), nil
	case nil:
		return 0, errors.New("is required when present")
	default:
		return 0, fmt.Errorf("unsupported type %T", value)
	}
	if n < int64(minInt()) || n > int64(maxInt()) {
		return 0, errors.New("overflows int")
	}
	return int(n), nil
}

func strictFloat(value any) (float64, error) {
	var f float64
	switch v := value.(type) {
	case float64:
		f = v
	case float32:
		f = float64(v)
	case int:
		f = float64(v)
	case int8:
		f = float64(v)
	case int16:
		f = float64(v)
	case int32:
		f = float64(v)
	case int64:
		f = float64(v)
	case uint:
		f = float64(v)
	case uint8:
		f = float64(v)
	case uint16:
		f = float64(v)
	case uint32:
		f = float64(v)
	case uint64:
		f = float64(v)
	case json.Number:
		parsed, err := strconv.ParseFloat(v.String(), 64)
		if err != nil {
			return 0, err
		}
		f = parsed
	case nil:
		return 0, errors.New("is required when present")
	default:
		return 0, fmt.Errorf("unsupported type %T", value)
	}
	if !isFinite(f) {
		return 0, errors.New("must be finite")
	}
	return f, nil
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func maxInt() int { return int(^uint(0) >> 1) }
func minInt() int { return -maxInt() - 1 }

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
