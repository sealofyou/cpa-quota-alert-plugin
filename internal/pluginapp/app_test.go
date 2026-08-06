package pluginapp

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/config"
)

func TestRegisterReturnsMetadataAndStoresConfig(t *testing.T) {
	app := New(func(string) string { return "" })
	raw, code := app.Call(MethodPluginRegister, lifecycleJSON(t, "dry_run: true\nconcurrency: 2\n"))
	if code != 0 {
		t.Fatalf("register code=%d response=%s", code, raw)
	}
	env := decodeEnvelope(t, raw)
	if !env.OK || env.Error != nil {
		t.Fatalf("unexpected envelope: %+v", env)
	}
	var result map[string]any
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("registration result: %v", err)
	}
	metadata := result["metadata"].(map[string]any)
	capabilities := result["capabilities"].(map[string]any)
	if result["schema_version"] != float64(1) || metadata["Name"] != "cpa-quota-alert-plugin" || metadata["Version"] != PluginVersion || capabilities["management_api"] != false {
		t.Fatalf("unexpected registration: %+v", result)
	}
	cfg, ok := app.Current()
	if !ok || !cfg.DryRun || cfg.Concurrency != 2 {
		t.Fatalf("stored config: ok=%t cfg=%+v", ok, cfg)
	}
}

func TestInvalidReconfigureKeepsPreviousConfigAndRedactsSource(t *testing.T) {
	app := New(func(string) string { return "" })
	if _, code := app.Call(MethodPluginRegister, lifecycleJSON(t, "concurrency: 2\n")); code != 0 {
		t.Fatal("initial register failed")
	}
	secret := "example-private-value"
	field := "smtp_" + "password"
	raw, code := app.Call(MethodPluginReconfigure, lifecycleJSON(t, field+": ["+secret))
	if code == 0 || strings.Contains(string(raw), secret) {
		t.Fatalf("invalid config code=%d response=%s", code, raw)
	}
	env := decodeEnvelope(t, raw)
	if env.Error == nil || env.Error.Code != "invalid_config" {
		t.Fatalf("unexpected error: %+v", env)
	}
	cfg, ok := app.Current()
	if !ok || cfg.Concurrency != 2 {
		t.Fatalf("previous config was not retained: %+v", cfg)
	}
}

func TestCurrentIsDeepCopyAndConcurrentReconfigureIsSafe(t *testing.T) {
	app := New(func(string) string { return "" })
	if _, code := app.Call(MethodPluginRegister, lifecycleJSON(t, "terminal_error_codes: [token_revoked]\n")); code != 0 {
		t.Fatal("register failed")
	}
	cfg, _ := app.Current()
	cfg.PlanRules["plus"] = config.PlanRule{Name: "mutated"}
	cfg.TerminalErrorCodes["mutated"] = struct{}{}
	again, _ := app.Current()
	if again.PlanRules["plus"].Name == "mutated" || again.TerminalErrorCodes.Contains("mutated") {
		t.Fatalf("Current returned aliased config: %+v", again)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = app.Current()
		}()
		go func(index int) {
			defer wg.Done()
			yaml := "concurrency: 2\n"
			if index%2 == 0 {
				yaml = "concurrency: 3\n"
			}
			if _, code := app.Call(MethodPluginReconfigure, lifecycleJSON(t, yaml)); code != 0 {
				t.Errorf("reconfigure code=%d", code)
			}
		}(i)
	}
	wg.Wait()
	final, ok := app.Current()
	if !ok || (final.Concurrency != 2 && final.Concurrency != 3) {
		t.Fatalf("invalid final config: %+v", final)
	}
}

func TestShutdownIsIdempotentAndPreventsReconfigure(t *testing.T) {
	app := New(func(string) string { return "" })
	_, _ = app.Call(MethodPluginRegister, nil)
	raw, code := app.Call(MethodPluginShutdown, nil)
	if code != 0 || !decodeEnvelope(t, raw).OK {
		t.Fatalf("shutdown failed: code=%d raw=%s", code, raw)
	}
	app.Shutdown()
	if _, ok := app.Current(); ok {
		t.Fatal("config remained visible after shutdown")
	}
	raw, code = app.Call(MethodPluginReconfigure, nil)
	if code == 0 || decodeEnvelope(t, raw).Error.Code != "plugin_shutdown" {
		t.Fatalf("reconfigure after shutdown: code=%d raw=%s", code, raw)
	}
}

func TestUnknownInvalidAndPanicAreStable(t *testing.T) {
	app := New(func(string) string { return "" })
	tests := []struct {
		method string
		want   string
		code   int
	}{
		{"", "invalid_method", 1},
		{"other.method", "unknown_method", 0},
	}
	for _, tt := range tests {
		raw, code := app.Call(tt.method, nil)
		env := decodeEnvelope(t, raw)
		if code != tt.code || env.Error == nil || env.Error.Code != tt.want {
			t.Fatalf("method=%q code=%d env=%+v", tt.method, code, env)
		}
	}

	app.parse = func([]byte, config.Getenv) (config.Config, error) { panic("example-private-value") }
	raw, code := app.Call(MethodPluginRegister, nil)
	if code == 0 || strings.Contains(string(raw), "example-private-value") || decodeEnvelope(t, raw).Error.Code != "plugin_panic" {
		t.Fatalf("panic was not redacted: code=%d raw=%s", code, raw)
	}
}

func TestLifecycleRequestIsStrict(t *testing.T) {
	app := New(func(string) string { return "" })
	for _, raw := range [][]byte{
		[]byte(`{"config_yaml":"","extra":true}`),
		[]byte(`{"config_yaml":`),
		[]byte(`{"config_yaml":""}{}`),
	} {
		response, code := app.Call(MethodPluginRegister, raw)
		if code == 0 || decodeEnvelope(t, response).Error.Code != "invalid_config" {
			t.Fatalf("request accepted: %s response=%s", raw, response)
		}
	}
}

func lifecycleJSON(t *testing.T, yaml string) []byte {
	t.Helper()
	raw, err := json.Marshal(lifecycleRequest{ConfigYAML: []byte(yaml)})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decodeEnvelope(t *testing.T, raw []byte) Envelope {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope %s: %v", raw, err)
	}
	return env
}
