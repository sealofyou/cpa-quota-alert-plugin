package management

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/config"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/secrets"
)

func TestUIResourceAndEffectiveSettingsNeverContainNotificationValues(t *testing.T) {
	h := newTestHandler(t)
	page := decodeResponse(t, mustCall(t, h, "GET", "/v0/resource/plugins/cpa-quota-alert-plugin/ui", nil))
	if page.StatusCode != 200 || !strings.Contains(page.Headers["Content-Type"][0], "text/html") || !bytes.Contains(page.Body, []byte("保存并核对生效")) {
		t.Fatalf("UI response: status=%d headers=%v", page.StatusCode, page.Headers)
	}
	if !bytes.Contains(page.Body, []byte("/v0/management/plugins/")) || !bytes.Contains(page.Body, []byte("/v0/management/cpa-quota-alert")) {
		t.Fatal("UI does not use protected management endpoints")
	}
	effective := decodeResponse(t, mustCall(t, h, "GET", "/cpa-quota-alert/effective-config", nil))
	if effective.StatusCode != 200 || bytes.Contains(effective.Body, []byte("SMTP_PASSWORD")) || bytes.Contains(effective.Body, []byte("test-only-password")) {
		t.Fatalf("effective settings exposed secret: %s", effective.Body)
	}
	var settings map[string]any
	if err := json.Unmarshal(effective.Body, &settings); err != nil || settings["low_threshold"] != 1.5 {
		t.Fatalf("effective settings: %v %v", settings, err)
	}
}

func TestEffectiveSettingsExposeEditableDefaultPlanCatalog(t *testing.T) {
	h := newTestHandler(t)
	cfg, err := config.Parse(map[string]any{}, nil)
	if err != nil {
		t.Fatalf("Parse defaults: %v", err)
	}
	h.deps.ConfigProvider = configProviderFunc(func(context.Context) (config.Config, bool) { return cfg, true })
	effective := decodeResponse(t, mustCall(t, h, "GET", "/cpa-quota-alert/effective-config", nil))
	if effective.StatusCode != 200 {
		t.Fatalf("effective status=%d body=%s", effective.StatusCode, effective.Body)
	}
	var settings struct {
		PlanRules []struct {
			Name    string   `json:"name"`
			Aliases []string `json:"aliases"`
			Window  string   `json:"window"`
			Weight  float64  `json:"weight"`
		} `json:"plan_rules"`
	}
	if err := json.Unmarshal(effective.Body, &settings); err != nil {
		t.Fatalf("decode effective settings: %v", err)
	}
	got := map[string]float64{}
	aliases := map[string]bool{}
	for _, rule := range settings.PlanRules {
		got[rule.Name] = rule.Weight
		for _, alias := range rule.Aliases {
			aliases[alias] = true
		}
	}
	for name, weight := range map[string]float64{"plus": 1, "team": 1, "pro100": 100, "pro200": 200, "pro500": 500, "prolite": 1} {
		if got[name] != weight {
			t.Fatalf("effective plan %s weight = %v, want %v; all=%+v", name, got[name], weight, settings.PlanRules)
		}
	}
	if aliases["pro"] {
		t.Fatalf("effective default catalog must not alias bare pro: %+v", settings.PlanRules)
	}
}

func TestSecretManagementReturnsNamesOnlyAndValidatesSettings(t *testing.T) {
	h := newTestHandler(t)
	store := &secrets.Store{Path: filepath.Join(t.TempDir(), "private", "notification-secrets.json")}
	h.deps.SecretStore = store
	h.deps.Getenv = func(string) string { return "test-only-password" }
	response := decodeResponse(t, mustCall(t, h, "PUT", "/cpa-quota-alert/secrets", map[string]any{"set": map[string]string{"CPA_QUOTA_ALERT_SMTP_PASSWORD": "test-only-password"}}))
	if response.StatusCode != 200 || bytes.Contains(response.Body, []byte("test-only-password")) {
		t.Fatalf("secret response: %s", response.Body)
	}
	read := decodeResponse(t, mustCall(t, h, "GET", "/cpa-quota-alert/secrets", nil))
	if read.StatusCode != 200 || !bytes.Contains(read.Body, []byte("CPA_QUOTA_ALERT_SMTP_PASSWORD")) || bytes.Contains(read.Body, []byte("test-only-password")) {
		t.Fatalf("secret read: %s", read.Body)
	}
	invalid := decodeResponse(t, mustCall(t, h, "POST", "/cpa-quota-alert/validate", map[string]any{"low_threshold": -1}))
	if invalid.StatusCode != 400 || !bytes.Contains(invalid.Body, []byte(`"field":"low_threshold"`)) {
		t.Fatalf("invalid settings: %s", invalid.Body)
	}
	valid := decodeResponse(t, mustCall(t, h, "POST", "/cpa-quota-alert/validate", map[string]any{"low_threshold": 1.5, "recovery_threshold": 1.6}))
	if valid.StatusCode != 200 {
		t.Fatalf("valid settings: %s", valid.Body)
	}
	ctx := context.Background()
	if _, ok := h.deps.ConfigProvider.Current(ctx); !ok {
		t.Fatal("test handler lost config")
	}
}

func TestValidatePendingNamesDoesNotStoreValues(t *testing.T) {
	h := newTestHandler(t)
	store := &secrets.Store{Path: filepath.Join(t.TempDir(), "private", "notification-secrets.json")}
	h.deps.SecretStore = store
	h.deps.Getenv = func(string) string { return "" }
	smtp := map[string]any{"enabled": true, "host": "smtp.example.invalid", "port": 587,
		"username_env": "SMTP_USERNAME", "password_env": "SMTP_PASSWORD",
		"recipients_env": "SMTP_RECIPIENTS", "from_env": "SMTP_FROM"}
	config := map[string]any{"smtp": smtp}
	missing := decodeResponse(t, mustCall(t, h, "POST", "/cpa-quota-alert/validate", config))
	if missing.StatusCode != 400 {
		t.Fatalf("missing notification values accepted: %s", missing.Body)
	}
	pending := map[string]string{"SMTP_USERNAME": "user", "SMTP_PASSWORD": "test-only-password", "SMTP_RECIPIENTS": "ops@example.com", "SMTP_FROM": "alerts@example.com"}
	valid := decodeResponse(t, mustCall(t, h, "POST", "/cpa-quota-alert/validate", map[string]any{"config": config, "pending_secrets": pending}))
	if valid.StatusCode != 200 {
		t.Fatalf("pending names did not validate: %s", valid.Body)
	}
	stored, err := store.Names()
	if err != nil || len(stored) != 0 {
		t.Fatalf("validation wrote to private store: %v %v", stored, err)
	}
}
