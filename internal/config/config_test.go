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
		case "SMTP_USERNAME", "SMTP_PASSWORD", "SMTP_RECIPIENTS", "SMTP_FROM", "WEBHOOK_URL", "WEBHOOK_AUTH":
			return "configured"
		default:
			return ""
		}
	}
	_, err := Parse(map[string]any{
		"smtp": map[string]any{
			"enabled":         true,
			"host":            "smtp.example.test",
			"port":            465,
			"tls_mode":        "implicit_tls",
			"timeout_seconds": 5,
			"username_env":    "SMTP_USERNAME",
			"password_env":    "SMTP_PASSWORD",
			"recipients_env":  "SMTP_RECIPIENTS",
			"from_env":        "SMTP_FROM",
			"from_name":       "CPA Quota Alert",
		},
		"webhook": map[string]any{
			"enabled":         true,
			"method":          "PUT",
			"timeout_seconds": 5,
			"url_env":         "WEBHOOK_URL",
			"auth_header_env": "WEBHOOK_AUTH",
		},
	}, getenv)
	if err != nil {
		t.Fatalf("Parse enabled notifications: %v", err)
	}
	want := map[string]bool{"SMTP_USERNAME": true, "SMTP_PASSWORD": true, "SMTP_RECIPIENTS": true, "SMTP_FROM": true, "WEBHOOK_URL": true, "WEBHOOK_AUTH": true}
	for _, call := range calls {
		delete(want, call)
	}
	if len(want) != 0 {
		t.Fatalf("getenv missing calls: %v, calls=%v", want, calls)
	}
}

func TestEnabledNotificationMissingEnvFailsWithoutValueEcho(t *testing.T) {
	_, err := Parse(map[string]any{
		"smtp": map[string]any{
			"enabled":         true,
			"host":            "smtp.example.test",
			"port":            587,
			"tls_mode":        "starttls",
			"timeout_seconds": 5,
			"username_env":    "SMTP_USERNAME",
			"password_env":    "SMTP_PASSWORD",
			"recipients_env":  "SMTP_RECIPIENTS",
			"from_env":        "SMTP_FROM",
		},
	}, func(key string) string {
		if key == "SMTP_PASSWORD" {
			return "test-secret-value"
		}
		return ""
	})
	if err == nil {
		t.Fatalf("expected missing env error")
	}
	if got := err.Error(); got == "" || contains(got, "test-secret-value") {
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

func TestNotificationSectionsMustBeObjectsWhenPresent(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "smtp bool", raw: map[string]any{"smtp": true}, want: "smtp must be an object"},
		{name: "smtp string", raw: map[string]any{"smtp": "disabled"}, want: "smtp must be an object"},
		{name: "smtp list", raw: map[string]any{"smtp": []any{}}, want: "smtp must be an object"},
		{name: "smtp null", raw: map[string]any{"smtp": nil}, want: "smtp must be an object"},
		{name: "webhook bool", raw: map[string]any{"webhook": false}, want: "webhook must be an object"},
		{name: "webhook string", raw: map[string]any{"webhook": "disabled"}, want: "webhook must be an object"},
		{name: "webhook list", raw: map[string]any{"webhook": []any{}}, want: "webhook must be an object"},
		{name: "webhook null", raw: map[string]any{"webhook": nil}, want: "webhook must be an object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.raw, nil)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}

func TestEnabledNotificationRejectsInvalidEnvNamesBeforeGetenv(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
	}{
		{
			name: "smtp bad name",
			raw: map[string]any{"smtp": map[string]any{
				"enabled":         true,
				"host":            "smtp.example.test",
				"port":            587,
				"tls_mode":        "starttls",
				"timeout_seconds": 5,
				"username_env":    "BAD NAME",
				"password_env":    "SMTP_PASSWORD",
				"recipients_env":  "SMTP_RECIPIENTS",
				"from_env":        "SMTP_FROM",
			}},
		},
		{
			name: "smtp equals",
			raw: map[string]any{"smtp": map[string]any{
				"enabled":         true,
				"host":            "smtp.example.test",
				"port":            587,
				"tls_mode":        "starttls",
				"timeout_seconds": 5,
				"username_env":    "SMTP_USERNAME",
				"password_env":    "A=B",
				"recipients_env":  "SMTP_RECIPIENTS",
				"from_env":        "SMTP_FROM",
			}},
		},
		{
			name: "webhook control",
			raw: map[string]any{"webhook": map[string]any{
				"enabled":         true,
				"method":          "POST",
				"timeout_seconds": 5,
				"url_env":         "WEBHOOK_URL\r\nINJECTED",
				"auth_header_env": "WEBHOOK_AUTH",
			}},
		},
		{
			name: "webhook digit start",
			raw: map[string]any{"webhook": map[string]any{
				"enabled":         true,
				"method":          "POST",
				"timeout_seconds": 5,
				"url_env":         "WEBHOOK_URL",
				"auth_header_env": "1WEBHOOK_AUTH",
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := []string{}
			_, err := Parse(tc.raw, func(key string) string {
				calls = append(calls, key)
				return "injected-runtime-secret"
			})
			if err == nil {
				t.Fatalf("expected invalid env name error")
			}
			if len(calls) != 0 {
				t.Fatalf("getenv must not be called for invalid env names, got %v", calls)
			}
			if got := err.Error(); contains(got, "injected-runtime-secret") || contains(got, "BAD NAME") || contains(got, "A=B") || contains(got, "INJECTED") {
				t.Fatalf("error must not echo invalid names or runtime secrets: %q", got)
			}
		})
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
	for _, key := range []string{"recipients", "recipient", "recipient_env", "username", "password", "from"} {
		_, err := Parse(map[string]any{"smtp": map[string]any{key: "alerts@example.com"}}, nil)
		if err == nil {
			t.Fatalf("expected smtp.%s rejection", key)
		}
	}
}

func TestNotificationConfigRejectsInvalidStrictFields(t *testing.T) {
	getenv := func(string) string { return "configured" }
	cases := []map[string]any{
		{"smtp": map[string]any{"enabled": true, "host": "smtp.example.test", "port": 0, "tls_mode": "starttls", "timeout_seconds": 5, "username_env": "U", "password_env": "P", "recipients_env": "R", "from_env": "F"}},
		{"smtp": map[string]any{"enabled": true, "host": "smtp.example.test", "port": 587, "tls_mode": "none", "timeout_seconds": 5, "username_env": "U", "password_env": "P", "recipients_env": "R", "from_env": "F"}},
		{"smtp": map[string]any{"enabled": true, "host": "smtp.example.test", "port": 587, "tls_mode": "starttls", "timeout_seconds": 0, "username_env": "U", "password_env": "P", "recipients_env": "R", "from_env": "F"}},
		{"smtp": map[string]any{"enabled": true, "host": "smtp.example.test", "port": 587, "tls_mode": "starttls", "timeout_seconds": 5, "username_env": "U", "password_env": "P", "recipients_env": "R", "from_env": "F", "extra": "nope"}},
		{"webhook": map[string]any{"enabled": true, "method": "GET", "timeout_seconds": 5, "url_env": "URL", "auth_header_env": "AUTH"}},
		{"webhook": map[string]any{"enabled": true, "method": "POST", "timeout_seconds": 0, "url_env": "URL", "auth_header_env": "AUTH"}},
		{"webhook": map[string]any{"enabled": true, "method": "POST", "timeout_seconds": 5, "url_env": "URL", "auth_header_env": "AUTH", "url": "https://example.test/hook"}},
		{"webhook": map[string]any{"enabled": true, "method": "POST", "timeout_seconds": 5, "url_env": "URL", "auth_header_env": "AUTH", "header": "Bearer token"}},
	}
	for _, raw := range cases {
		if _, err := Parse(raw, getenv); err == nil {
			t.Fatalf("expected notification config error for %#v", raw)
		}
	}
}

func TestNotificationConfigDefaults(t *testing.T) {
	cfg, err := Parse(map[string]any{
		"smtp": map[string]any{
			"enabled":         true,
			"host":            "smtp.example.test",
			"port":            465,
			"tls_mode":        "implicit_tls",
			"timeout_seconds": 5,
			"username_env":    "U",
			"password_env":    "P",
			"recipients_env":  "R",
			"from_env":        "F",
		},
		"webhook": map[string]any{
			"enabled":         true,
			"timeout_seconds": 5,
			"url_env":         "URL",
			"auth_header_env": "AUTH",
		},
	}, func(string) string { return "configured" })
	if err != nil {
		t.Fatalf("Parse notification defaults: %v", err)
	}
	if cfg.SMTP.TLSMode != "implicit_tls" || cfg.Webhook.Method != "POST" {
		t.Fatalf("unexpected notification defaults: smtp=%+v webhook=%+v", cfg.SMTP, cfg.Webhook)
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
