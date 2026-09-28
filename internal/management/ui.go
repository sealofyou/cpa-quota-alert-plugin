package management

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/config"
)

//go:embed ui.html
var uiFiles embed.FS

func uiResponse() Response {
	html, err := uiFiles.ReadFile("ui.html")
	if err != nil {
		return errorResponse(503, "ui_unavailable")
	}
	return Response{StatusCode: 200, Headers: map[string][]string{
		"Content-Type":            {"text/html; charset=utf-8"},
		"Cache-Control":           {"no-store"},
		"X-Content-Type-Options":  {"nosniff"},
		"Content-Security-Policy": {"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"},
	}, Body: html}
}

func (h *Handler) effectiveConfig(ctx context.Context) Response {
	cfg, ok := h.deps.ConfigProvider.Current(ctx)
	if !ok {
		return errorResponse(503, "not_configured")
	}
	return jsonResponse(200, effectiveSettings(cfg))
}

func effectiveSettings(cfg config.Config) map[string]any {
	rules := make([]map[string]any, 0, len(cfg.PlanRules))
	for _, rule := range cfg.PlanRules {
		rules = append(rules, map[string]any{"name": rule.Name, "aliases": rule.Aliases, "window": rule.Window, "weight": rule.Weight})
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i]["name"].(string) < rules[j]["name"].(string) })
	templates := map[string]any{}
	for kind, item := range cfg.Mail.Templates {
		templates[kind] = map[string]any{"subject": item.Subject, "body": item.Body}
	}
	return map[string]any{
		"dry_run": cfg.DryRun, "concurrency": cfg.Concurrency,
		"request_timeout_seconds": cfg.RequestTimeoutSeconds, "retry_attempts": cfg.RetryAttempts,
		"stale_after_seconds": cfg.StaleAfterSeconds, "low_threshold": cfg.LowThreshold,
		"recovery_threshold": cfg.RecoveryThreshold, "reminder_seconds": cfg.ReminderSeconds,
		"failure_alert_count": cfg.FailureAlertCount, "state_path": cfg.StatePath,
		"quota_url": cfg.QuotaURL, "allowed_quota_hosts": sortedHostSet(cfg.AllowedQuotaHosts),
		"ignored_plans": sortedPlanSet(cfg.IgnoredPlans), "terminal_error_codes": sortedStringSet(cfg.TerminalErrorCodes),
		"plan_rules": rules,
		"smtp": map[string]any{
			"enabled": cfg.SMTP.Enabled, "host": cfg.SMTP.Host, "port": cfg.SMTP.Port,
			"tls_mode": cfg.SMTP.TLSMode, "timeout_seconds": cfg.SMTP.TimeoutSeconds,
			"username_env": cfg.SMTP.UsernameEnv, "password_env": cfg.SMTP.PasswordEnv,
			"recipients_env": cfg.SMTP.RecipientsEnv, "from_env": cfg.SMTP.FromEnv,
			"from_name": cfg.SMTP.FromName,
		},
		"webhook": map[string]any{
			"enabled": cfg.Webhook.Enabled, "method": cfg.Webhook.Method,
			"timeout_seconds": cfg.Webhook.TimeoutSeconds,
			"url_env":         cfg.Webhook.URLEnv, "auth_header_env": cfg.Webhook.AuthHeaderEnv,
		},
		"mail": map[string]any{"templates": templates},
	}
}

func sortedHostSet(in config.HostSet) []string {
	out := make([]string, 0, len(in))
	for value := range in {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
func sortedPlanSet(in config.PlanSet) []string {
	out := make([]string, 0, len(in))
	for value := range in {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
func sortedStringSet(in config.StringSet) []string {
	out := make([]string, 0, len(in))
	for value := range in {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

type secretUpdate struct {
	Set   map[string]string `json:"set"`
	Clear []string          `json:"clear"`
}

func (h *Handler) secrets(ctx context.Context, req Request) Response {
	store := h.deps.SecretStore
	if store == nil {
		return errorResponse(503, "private_store_unavailable")
	}
	if req.Method == "GET" {
		if len(bytes.TrimSpace(req.Body)) != 0 {
			return errorResponse(400, "unsupported_body")
		}
		names, err := store.Names()
		if err != nil {
			return errorResponse(503, "private_store_unavailable")
		}
		return jsonResponse(200, map[string]any{"stored_names": names})
	}
	if req.Method != "PUT" {
		return errorResponse(405, "method_not_allowed")
	}
	dec := json.NewDecoder(bytes.NewReader(req.Body))
	dec.DisallowUnknownFields()
	var update secretUpdate
	if err := dec.Decode(&update); err != nil {
		return errorResponse(400, "invalid_request")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errorResponse(400, "invalid_request")
	}
	if len(update.Set)+len(update.Clear) > 16 {
		return errorResponse(400, "invalid_request")
	}
	cfg, ok := h.deps.ConfigProvider.Current(ctx)
	if !ok {
		return errorResponse(503, "not_configured")
	}
	allowed := map[string]bool{}
	for _, name := range []string{cfg.SMTP.UsernameEnv, cfg.SMTP.PasswordEnv, cfg.SMTP.RecipientsEnv, cfg.SMTP.FromEnv, cfg.Webhook.URLEnv, cfg.Webhook.AuthHeaderEnv} {
		if name != "" {
			allowed[name] = true
		}
	}
	for name := range update.Set {
		if !allowed[name] && !strings.HasPrefix(name, "CPA_QUOTA_ALERT_") {
			return errorResponse(400, "invalid_notification_name")
		}
	}
	for _, name := range update.Clear {
		if !allowed[name] && !strings.HasPrefix(name, "CPA_QUOTA_ALERT_") {
			return errorResponse(400, "invalid_notification_name")
		}
	}
	if err := store.Update(update.Set, update.Clear); err != nil {
		return errorResponse(400, "private_store_update_failed")
	}
	names, err := store.Names()
	if err != nil {
		return errorResponse(503, "private_store_unavailable")
	}
	return jsonResponse(200, map[string]any{"stored_names": names})
}

func (h *Handler) validateSettings(req Request) Response {
	var raw map[string]any
	if err := json.Unmarshal(req.Body, &raw); err != nil || raw == nil {
		return errorResponse(400, "invalid_request")
	}
	getenv := h.deps.Getenv
	if candidate, wrapped := raw["config"]; wrapped {
		cfg, ok := candidate.(map[string]any)
		if !ok {
			return errorResponse(400, "invalid_request")
		}
		raw = cfg
		var request struct {
			PendingSecrets map[string]string `json:"pending_secrets"`
			PendingClear   []string          `json:"pending_clear"`
		}
		if err := json.Unmarshal(req.Body, &request); err != nil || len(request.PendingSecrets)+len(request.PendingClear) > 16 {
			return errorResponse(400, "invalid_request")
		}
		clear := map[string]bool{}
		for _, name := range request.PendingClear {
			if name == "" || len(name) > 128 {
				return errorResponse(400, "invalid_request")
			}
			if _, found := request.PendingSecrets[name]; found {
				return errorResponse(400, "invalid_request")
			}
			clear[name] = true
		}
		for name, value := range request.PendingSecrets {
			if name == "" || len(name) > 128 || strings.TrimSpace(value) == "" || len(value) > 4096 || strings.ContainsAny(value, "\r\n\x00") {
				return errorResponse(400, "invalid_request")
			}
		}
		getenv = func(name string) string {
			if value, found := request.PendingSecrets[name]; found {
				return value
			}
			if clear[name] {
				if h.deps.BaseGetenv != nil {
					return h.deps.BaseGetenv(name)
				}
				return ""
			}
			if h.deps.Getenv != nil {
				return h.deps.Getenv(name)
			}
			return ""
		}
	}
	parsed, err := config.Parse(raw, getenv)
	if err != nil {
		message := err.Error()
		field := "settings"
		for _, candidate := range []string{"dry_run", "concurrency", "request_timeout_seconds", "retry_attempts", "stale_after_seconds", "low_threshold", "recovery_threshold", "reminder_seconds", "failure_alert_count", "state_path", "quota_url", "allowed_quota_hosts", "ignored_plans", "terminal_error_codes", "plan_rules", "smtp", "webhook", "mail"} {
			if strings.Contains(message, candidate) {
				field = candidate
				break
			}
		}
		return jsonResponse(400, map[string]any{"error_code": "invalid_config", "field": field})
	}
	if _, err := h.deps.ChannelFactory.Channels(parsed); err != nil {
		return jsonResponse(400, map[string]any{"error_code": "invalid_notification_config", "field": "smtp"})
	}
	return jsonResponse(200, map[string]any{"valid": true})
}
