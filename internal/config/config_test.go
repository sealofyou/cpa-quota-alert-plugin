package config

import (
	"encoding/json"
	"math"
	"testing"
)

func TestDefaultsAndValidation(t *testing.T) {
	cfg, err := Parse(map[string]any{}, nil)
	if err != nil {
		t.Fatalf("Parse defaults: %v", err)
	}
	if cfg.Concurrency != 4 || cfg.RequestTimeoutSeconds != 45 || cfg.RetryAttempts != 2 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.PlanRules["plus"].Weight != 1 || cfg.PlanRules["plus"].Window != Window7d {
		t.Fatalf("missing conservative plus default: %+v", cfg.PlanRules)
	}
	if _, ok := cfg.PlanRules["pro"]; ok {
		t.Fatalf("pro must not be a default plan rule")
	}
	if !cfg.IgnoredPlans.Contains("free") {
		t.Fatalf("free must be ignored by default")
	}
}

func TestExplicitRulesAliasesConflictsAndSecrets(t *testing.T) {
	_, err := Parse(map[string]any{
		"plan_rules": []any{
			map[string]any{"name": "plus", "aliases": []any{"Plus", "p_l-u s"}, "window": Window7d, "weight": 1},
			map[string]any{"name": "team", "aliases": []any{"plus"}, "window": Window7d, "weight": 1},
		},
	}, nil)
	if err == nil {
		t.Fatalf("expected alias conflict")
	}
	_, err = Parse(map[string]any{"ignored_plans": []any{"free"}, "plan_rules": []any{
		map[string]any{"name": "plus", "aliases": []any{"free"}, "window": Window7d, "weight": 1},
	}}, nil)
	if err == nil {
		t.Fatalf("expected ignored alias conflict")
	}
	_, err = Parse(map[string]any{"smtp": map[string]any{"smtp_password": "plain"}}, nil)
	if err == nil {
		t.Fatalf("expected plaintext secret rejection")
	}
}

func TestExplicitK12AndProAllowedOnlyWhenConfigured(t *testing.T) {
	cfg, err := Parse(map[string]any{"plan_rules": []any{
		map[string]any{"name": "plus", "aliases": []any{"plus"}, "window": Window7d, "weight": 1},
		map[string]any{"name": "k12", "aliases": []any{"k-12"}, "window": Window7d, "weight": 0.2},
		map[string]any{"name": "pro", "aliases": []any{"pro20"}, "window": Window7d, "weight": 20},
	}}, nil)
	if err != nil {
		t.Fatalf("Parse explicit rules: %v", err)
	}
	if cfg.PlanRules["k12"].Weight != 0.2 || cfg.PlanRules["pro"].Weight != 20 {
		t.Fatalf("explicit weights not preserved: %+v", cfg.PlanRules)
	}
}

func TestThresholdAndURLValidation(t *testing.T) {
	if _, err := Parse(map[string]any{"low_threshold": 1.6, "recovery_threshold": 1.5}, nil); err == nil {
		t.Fatalf("expected threshold validation error")
	}
	if _, err := Parse(map[string]any{"quota_url": "/relative"}, nil); err == nil {
		t.Fatalf("expected absolute URL validation error")
	}
	if _, err := Parse(map[string]any{"quota_url": "https://example.com/usage"}, nil); err == nil {
		t.Fatalf("expected host allowlist validation error")
	}
}

func TestDefaultsExcludeProAndK12(t *testing.T) {
	cfg, err := Parse(map[string]any{}, nil)
	if err != nil {
		t.Fatalf("Parse defaults: %v", err)
	}
	if _, ok := cfg.PlanRules["pro"]; ok {
		t.Fatalf("pro must not be a default plan rule")
	}
	if _, ok := cfg.PlanRules["k12"]; ok {
		t.Fatalf("k12 must not be a default plan rule")
	}
}

func TestEnabledNotificationEnvValidation(t *testing.T) {
	calls := []string{}
	getenv := func(key string) string {
		calls = append(calls, key)
		switch key {
		case "SMTP_PASSWORD", "SMTP_RECIPIENTS", "WEBHOOK_URL", "WEBHOOK_AUTH":
			return "configured"
		default:
			return ""
		}
	}
	_, err := Parse(map[string]any{
		"smtp":    map[string]any{"enabled": true, "password_env": "SMTP_PASSWORD", "recipients_env": "SMTP_RECIPIENTS"},
		"webhook": map[string]any{"enabled": true, "url_env": "WEBHOOK_URL", "auth_header_env": "WEBHOOK_AUTH"},
	}, getenv)
	if err != nil {
		t.Fatalf("Parse enabled notifications: %v", err)
	}
	want := map[string]bool{"SMTP_PASSWORD": true, "SMTP_RECIPIENTS": true, "WEBHOOK_URL": true, "WEBHOOK_AUTH": true}
	for _, call := range calls {
		delete(want, call)
	}
	if len(want) != 0 {
		t.Fatalf("getenv missing calls: %v, calls=%v", want, calls)
	}
}

func TestEnabledNotificationMissingEnvFailsWithoutValueEcho(t *testing.T) {
	_, err := Parse(map[string]any{
		"smtp": map[string]any{"enabled": true, "password_env": "SMTP_PASSWORD", "recipients_env": "SMTP_RECIPIENTS"},
	}, func(key string) string {
		if key == "SMTP_PASSWORD" {
			return "super-secret-value"
		}
		return ""
	})
	if err == nil {
		t.Fatalf("expected missing env error")
	}
	if got := err.Error(); got == "" || contains(got, "super-secret-value") {
		t.Fatalf("error must be stable and must not echo env value: %q", got)
	}
}

func TestDisabledNotificationDoesNotCallGetenv(t *testing.T) {
	called := false
	_, err := Parse(map[string]any{
		"smtp":    map[string]any{"enabled": false, "password_env": "SMTP_PASSWORD", "recipients_env": "SMTP_RECIPIENTS"},
		"webhook": map[string]any{"enabled": false, "url_env": "WEBHOOK_URL", "auth_header_env": "WEBHOOK_AUTH"},
	}, func(string) string { called = true; return "" })
	if err != nil {
		t.Fatalf("Parse disabled notifications: %v", err)
	}
	if called {
		t.Fatalf("getenv must not be called for disabled notifications")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func TestStrictTypedReadersRejectInvalidValues(t *testing.T) {
	bad := []map[string]any{
		{"concurrency": "4"},
		{"concurrency": 1.2},
		{"concurrency": true},
		{"concurrency": nil},
		{"concurrency": uint64(^uint64(0))},
		{"low_threshold": "1.5"},
		{"low_threshold": true},
	}
	for _, raw := range bad {
		if _, err := Parse(raw, nil); err == nil {
			t.Fatalf("expected typed parse error for %#v", raw)
		}
	}
	cfg, err := Parse(map[string]any{"concurrency": 4.0, "retry_attempts": 0, "low_threshold": 1, "recovery_threshold": 1.1}, nil)
	if err != nil {
		t.Fatalf("integral float and numeric thresholds should parse: %v", err)
	}
	if cfg.Concurrency != 4 || cfg.RetryAttempts != 0 {
		t.Fatalf("unexpected parsed config: %+v", cfg)
	}
}

func TestRangeValidation(t *testing.T) {
	bad := []map[string]any{
		{"concurrency": 0},
		{"request_timeout_seconds": -1},
		{"stale_after_seconds": 0},
		{"reminder_seconds": 0},
		{"failure_alert_count": 0},
		{"retry_attempts": -1},
		{"state_path": ""},
		{"low_threshold": 0},
		{"low_threshold": 1.5, "recovery_threshold": 1.5},
	}
	for _, raw := range bad {
		if _, err := Parse(raw, nil); err == nil {
			t.Fatalf("expected range error for %#v", raw)
		}
	}
	cfg, err := Parse(map[string]any{}, nil)
	if err != nil {
		t.Fatalf("defaults should parse: %v", err)
	}
	if cfg.StatePath == "" {
		t.Fatalf("default state_path must be non-empty")
	}
}

func TestSMTPPlainRecipientsRejected(t *testing.T) {
	for _, key := range []string{"recipients", "recipient", "recipient_env"} {
		_, err := Parse(map[string]any{"smtp": map[string]any{key: []any{"alerts@example.com"}}}, nil)
		if err == nil {
			t.Fatalf("expected smtp.%s rejection", key)
		}
	}
}

func TestJSONNumberAndNonFiniteValidation(t *testing.T) {
	cfg, err := Parse(map[string]any{"concurrency": json.Number("4.0")}, nil)
	if err != nil {
		t.Fatalf("integral json.Number should parse: %v", err)
	}
	if cfg.Concurrency != 4 {
		t.Fatalf("unexpected concurrency: %d", cfg.Concurrency)
	}
	for _, raw := range []map[string]any{
		{"concurrency": json.Number("4.5")},
		{"low_threshold": math.NaN()},
		{"low_threshold": math.Inf(1)},
	} {
		if _, err := Parse(raw, nil); err == nil {
			t.Fatalf("expected numeric validation error for %#v", raw)
		}
	}
}

func TestAllowedQuotaHostsUseDNSHostNormalizationOnly(t *testing.T) {
	docIP := "127." + "0.0.1"
	_, err := Parse(map[string]any{
		"quota_url":           "https://safeapi.example/backend-api/wham/usage",
		"allowed_quota_hosts": []any{"safe-api.example"},
	}, nil)
	if err == nil {
		t.Fatalf("safe-api.example must not match safeapi.example")
	}
	for _, tc := range []struct {
		url  string
		host string
	}{
		{url: "https://SAFE-API.example./usage", host: " safe-api.example.. "},
		{url: "https://LOCALHOST./usage", host: "localhost"},
		{url: "https://" + docIP + "/usage", host: docIP + "."},
	} {
		_, err := Parse(map[string]any{"quota_url": tc.url, "allowed_quota_hosts": []any{tc.host}}, nil)
		if err != nil {
			t.Fatalf("expected normalized host match for %+v: %v", tc, err)
		}
	}
}

func TestCollectionParsersRejectNonStringElements(t *testing.T) {
	cases := []map[string]any{
		{"terminal_error_codes": []any{"401", 403}},
		{"terminal_error_codes": []any{"401", nil}},
		{"ignored_plans": []any{"free", true}},
		{"allowed_quota_hosts": []any{"chatgpt.com", map[string]any{"host": "example.com"}}},
		{"plan_rules": []any{map[string]any{"name": "plus", "aliases": []any{"plus", 1}, "window": Window7d, "weight": 1}}},
	}
	for _, raw := range cases {
		_, err := Parse(raw, nil)
		if err == nil {
			t.Fatalf("expected strict collection error for %#v", raw)
		}
		if got := err.Error(); !contains(got, "[") {
			t.Fatalf("collection error should include field path/index, got %q", got)
		}
	}
	_, err := Parse(map[string]any{"terminal_error_codes": "401"}, nil)
	if err == nil {
		t.Fatalf("single string collection must be rejected")
	}
}

func TestTerminalCodesDoNotCoerceNumbers(t *testing.T) {
	_, err := Parse(map[string]any{"terminal_error_codes": []any{401}}, nil)
	if err == nil {
		t.Fatalf("numeric terminal code must not be coerced to string")
	}
}
