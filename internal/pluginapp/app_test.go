package pluginapp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/abi"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/config"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/management"
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
	if result["schema_version"] != float64(1) || metadata["Name"] != "cpa-quota-alert-plugin" || metadata["Version"] != PluginVersion || capabilities["management_api"] != true {
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

func TestLifecycleSchemaVersionsAndUnsupportedReconfigure(t *testing.T) {
	app := New(func(string) string { return "" })
	for _, schema := range []uint32{1, 2} {
		response, code := app.Call(MethodPluginRegister, lifecycleJSONSchema(t, schema, "concurrency: 2\n"))
		env := decodeEnvelope(t, response)
		if code != 0 || !env.OK {
			t.Fatalf("schema %d rejected: code=%d response=%s", schema, code, response)
		}
		var result map[string]any
		if err := json.Unmarshal(env.Result, &result); err != nil {
			t.Fatalf("registration result: %v", err)
		}
		if result["schema_version"] != float64(1) {
			t.Fatalf("response schema changed: %+v", result)
		}
	}
	if _, code := app.Call(MethodPluginRegister, lifecycleJSONSchema(t, 0, "concurrency: 4\n")); code != 0 {
		t.Fatal("schema 0 compatibility register failed")
	}
	raw, code := app.Call(MethodPluginReconfigure, lifecycleJSONSchema(t, 3, "concurrency: 9\n"))
	if code == 0 || decodeEnvelope(t, raw).Error.Code != "invalid_config" {
		t.Fatalf("unsupported schema accepted: code=%d raw=%s", code, raw)
	}
	cfg, ok := app.Current()
	if !ok || cfg.Concurrency != 4 {
		t.Fatalf("unsupported schema replaced previous config: ok=%t cfg=%+v", ok, cfg)
	}
}

func TestManagementRegisterAndHandleArePluginEnveloped(t *testing.T) {
	app := New(func(string) string { return "" })
	if _, code := app.Call(MethodPluginRegister, lifecycleJSON(t, "dry_run: true\n")); code != 0 {
		t.Fatal("plugin register failed")
	}
	invalid, invalidCode := app.Call(management.MethodManagementRegister, []byte(`{} trailing`))
	if invalidCode == 0 || decodeEnvelope(t, invalid).Error.Code != "invalid_request" {
		t.Fatalf("invalid management register request accepted: code=%d raw=%s", invalidCode, invalid)
	}
	raw, code := app.Call(management.MethodManagementRegister, []byte(`{"security_metadata":{"ignored":true}}`))
	env := decodeEnvelope(t, raw)
	if code != 0 || !env.OK {
		t.Fatalf("management register failed: code=%d raw=%s", code, raw)
	}
	var reg management.Registration
	if err := json.Unmarshal(env.Result, &reg); err != nil {
		t.Fatalf("management registration: %v", err)
	}
	want := []management.Route{
		{Method: "POST", Path: "/cpa-quota-alert/check", Description: "Run a CPA quota check."},
		{Method: "GET", Path: "/cpa-quota-alert/status", Description: "Read CPA quota alert status."},
		{Method: "POST", Path: "/cpa-quota-alert/test-notification", Description: "Send a CPA quota alert test notification."},
	}
	if len(reg.Resources) != 0 || len(reg.Routes) != len(want) {
		t.Fatalf("unexpected registration: %+v", reg)
	}
	for i := range want {
		if reg.Routes[i] != want[i] {
			t.Fatalf("route[%d]=%+v want %+v", i, reg.Routes[i], want[i])
		}
	}

	req := managementRequestJSON(t, "GET", "/cpa-quota-alert/check", nil, "host-1")
	raw, code = app.Call(management.MethodManagementHandle, req)
	env = decodeEnvelope(t, raw)
	if code != 0 || !env.OK {
		t.Fatalf("management HTTP error must remain plugin-ok: code=%d raw=%s", code, raw)
	}
	var resp management.Response
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("management response: %v", err)
	}
	if resp.StatusCode != 405 || !json.Valid(resp.Body) {
		t.Fatalf("unexpected response wire: %+v body=%s", resp, resp.Body)
	}
	var wire map[string]any
	if err := json.Unmarshal(env.Result, &wire); err != nil {
		t.Fatal(err)
	}
	if _, ok := wire["Body"].(string); !ok {
		t.Fatalf("response Body must marshal as base64 string: %s", env.Result)
	}
}

func TestManagementCheckUsesABIHostFactoryAndState(t *testing.T) {
	statePath := strings.ReplaceAll(t.TempDir(), `\`, `/`) + "/state.json"
	fake := &fakeABICaller{t: t, hostCallbackID: "cb-123"}
	var gotCallbackID string
	app := NewWithHost(func(string) string { return "" }, func(hostCallbackID string) *abi.Client {
		gotCallbackID = hostCallbackID
		return abi.NewClient(fake, hostCallbackID)
	})
	yaml := "dry_run: true\nconcurrency: 1\nretry_attempts: 0\nrequest_timeout_seconds: 1\nstate_path: " + statePath + "\n"
	if raw, code := app.Call(MethodPluginRegister, lifecycleJSONSchema(t, 2, yaml)); code != 0 {
		t.Fatalf("register failed: %s", raw)
	}
	req := managementRequestJSON(t, "POST", "/cpa-quota-alert/check", map[string]any{"dry_run": true}, "cb-123")
	raw, code := app.Call(management.MethodManagementHandle, req)
	env := decodeEnvelope(t, raw)
	if code != 0 || !env.OK {
		t.Fatalf("check failed: code=%d raw=%s", code, raw)
	}
	var resp management.Response
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("response: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if gotCallbackID != "cb-123" || body["total"] != 0.75 || body["selected"] != float64(1) || body["would_notify"] != false {
		t.Fatalf("unexpected check result: callback=%q body=%+v", gotCallbackID, body)
	}
	if fake.calls[abi.MethodAuthList] != 1 || fake.calls[abi.MethodAuthGet] != 1 || fake.calls[abi.MethodHTTPDo] != 1 {
		t.Fatalf("unexpected callback counts: %+v", fake.calls)
	}
	_, _ = app.Call(MethodPluginShutdown, nil)
	raw, code = app.Call(management.MethodManagementHandle, req)
	env = decodeEnvelope(t, raw)
	if code == 0 || env.Error == nil || env.Error.Code != "plugin_shutdown" {
		t.Fatalf("management after shutdown: code=%d raw=%s", code, raw)
	}
}

func TestManagementRuntimeFactoryErrorsAreStable(t *testing.T) {
	nilHostApp := New(func(string) string { return "" })
	statePath := strings.ReplaceAll(t.TempDir(), `\`, `/`) + "/state.json"
	if raw, code := nilHostApp.Call(MethodPluginRegister, lifecycleJSON(t, "dry_run: true\nstate_path: "+statePath+"\n")); code != 0 {
		t.Fatalf("nil-host register failed: %s", raw)
	}
	checkReq := managementRequestJSON(t, "POST", "/cpa-quota-alert/check", map[string]any{"dry_run": true}, "cb-err")
	raw, code := nilHostApp.Call(management.MethodManagementHandle, checkReq)
	env := decodeEnvelope(t, raw)
	if code != 0 || !env.OK {
		t.Fatalf("nil-host check failed at plugin layer: code=%d raw=%s", code, raw)
	}
	var nilHostResp management.Response
	if err := json.Unmarshal(env.Result, &nilHostResp); err != nil {
		t.Fatalf("nil-host response: %v", err)
	}
	if nilHostResp.StatusCode != 500 || !strings.Contains(string(nilHostResp.Body), "checker_unavailable") {
		t.Fatalf("nil-host error not stable: status=%d body=%s", nilHostResp.StatusCode, nilHostResp.Body)
	}

	app := NewWithHost(func(name string) string {
		if name == "SMTP_RECIPIENTS" {
			return "not an address"
		}
		if name == "SMTP_USERNAME" || name == "SMTP_PASSWORD" || name == "SMTP_FROM" {
			return "configured"
		}
		return ""
	}, func(string) *abi.Client { return nil })
	yaml := strings.Join([]string{
		"dry_run: true",
		"state_path: " + strings.ReplaceAll(t.TempDir(), `\`, `/`) + "/state.json",
		"smtp:",
		"  enabled: true",
		"  host: smtp.example.invalid",
		"  port: 587",
		"  username_env: SMTP_USERNAME",
		"  password_env: SMTP_PASSWORD",
		"  recipients_env: SMTP_RECIPIENTS",
		"  from_env: SMTP_FROM",
		"",
	}, "\n")
	if raw, code := app.Call(MethodPluginRegister, lifecycleJSON(t, yaml)); code != 0 {
		t.Fatalf("register failed: %s", raw)
	}
	req := managementRequestJSON(t, "GET", "/cpa-quota-alert/status", nil, "cb-err")
	raw, code = app.Call(management.MethodManagementHandle, req)
	env = decodeEnvelope(t, raw)
	if code != 0 || !env.OK {
		t.Fatalf("status call failed at plugin layer: code=%d raw=%s", code, raw)
	}
	var resp management.Response
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("response: %v", err)
	}
	if resp.StatusCode != 500 || strings.Contains(string(resp.Body), "not an address") || !strings.Contains(string(resp.Body), "channel_unavailable") {
		t.Fatalf("runtime channel error leaked or wrong: status=%d body=%s", resp.StatusCode, resp.Body)
	}
}

func TestShutdownCancelsAndWaitsForInflightManagement(t *testing.T) {
	statePath := strings.ReplaceAll(t.TempDir(), `\`, `/`) + "/state.json"
	caller := &blockingListCaller{
		entered:     make(chan struct{}),
		canceled:    make(chan struct{}),
		allowReturn: make(chan struct{}),
	}
	app := NewWithHost(func(string) string { return "" }, func(hostCallbackID string) *abi.Client {
		return abi.NewClient(caller, hostCallbackID)
	})
	if raw, code := app.Call(MethodPluginRegister, lifecycleJSON(t, "dry_run: true\nstate_path: "+statePath+"\n")); code != 0 {
		t.Fatalf("register failed: %s", raw)
	}

	callDone := make(chan struct{})
	var raw []byte
	var code int
	go func() {
		defer close(callDone)
		raw, code = app.Call(management.MethodManagementHandle, managementRequestJSON(t, "POST", "/cpa-quota-alert/check", map[string]any{"dry_run": true}, "cb-block"))
	}()
	<-caller.entered

	shutdownDone := make(chan struct{})
	go func() {
		app.ShutdownAndWait()
		close(shutdownDone)
	}()
	<-caller.canceled
	select {
	case <-shutdownDone:
		t.Fatal("Shutdown returned before inflight management call exited")
	default:
	}
	close(caller.allowReturn)
	<-shutdownDone
	<-callDone

	if code != 0 {
		t.Fatalf("management call became ABI failure: code=%d raw=%s", code, raw)
	}
	env := decodeEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("management call did not return stable envelope: %s", raw)
	}
	if caller.getCalls != 0 || caller.httpCalls != 0 {
		t.Fatalf("continued after cancellation: get=%d http=%d", caller.getCalls, caller.httpCalls)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("state should not be written after shutdown cancellation: %v", err)
	}
}

func TestReentrantShutdownFromHostCallbackDoesNotDeadlock(t *testing.T) {
	statePath := strings.ReplaceAll(t.TempDir(), `\`, `/`) + "/state.json"
	caller := &reentrantShutdownCaller{
		started:  make(chan struct{}),
		returned: make(chan struct{}),
	}
	app := NewWithHost(func(string) string { return "" }, func(hostCallbackID string) *abi.Client {
		return abi.NewClient(caller, hostCallbackID)
	})
	caller.app = app
	if raw, code := app.Call(MethodPluginRegister, lifecycleJSON(t, "dry_run: true\nstate_path: "+statePath+"\n")); code != 0 {
		t.Fatalf("register failed: %s", raw)
	}

	callDone := make(chan struct{})
	var raw []byte
	var code int
	go func() {
		defer close(callDone)
		raw, code = app.Call(management.MethodManagementHandle, managementRequestJSON(t, "POST", "/cpa-quota-alert/check", map[string]any{"dry_run": true}, "cb-reentrant"))
	}()

	<-caller.started
	select {
	case <-caller.returned:
	case <-time.After(time.Second):
		t.Fatal("host callback did not return after reentrant Shutdown")
	}
	select {
	case <-callDone:
	case <-time.After(time.Second):
		t.Fatal("management call deadlocked after reentrant Shutdown")
	}
	app.Wait()
	if code != 0 {
		t.Fatalf("management call became ABI failure: code=%d raw=%s", code, raw)
	}
	env := decodeEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("management call did not return stable envelope: %s", raw)
	}
	if caller.getCalls != 0 || caller.httpCalls != 0 {
		t.Fatalf("continued after reentrant shutdown: get=%d http=%d", caller.getCalls, caller.httpCalls)
	}
	if _, ok := app.Current(); ok {
		t.Fatal("app remained configured after reentrant shutdown")
	}
}

func TestManagementCallWithStaleAppPointerAfterShutdownIsRejected(t *testing.T) {
	app := NewWithHost(func(string) string { return "" }, func(hostCallbackID string) *abi.Client {
		return abi.NewClient(&fakeABICaller{t: t, hostCallbackID: hostCallbackID}, hostCallbackID)
	})
	statePath := strings.ReplaceAll(t.TempDir(), `\`, `/`) + "/state.json"
	if raw, code := app.Call(MethodPluginRegister, lifecycleJSON(t, "dry_run: true\nstate_path: "+statePath+"\n")); code != 0 {
		t.Fatalf("register failed: %s", raw)
	}
	app.Shutdown()
	raw, code := app.Call(management.MethodManagementHandle, managementRequestJSON(t, "POST", "/cpa-quota-alert/check", map[string]any{"dry_run": true}, "cb-stale"))
	env := decodeEnvelope(t, raw)
	if code == 0 || env.Error == nil || env.Error.Code != "plugin_shutdown" {
		t.Fatalf("stale app pointer was not rejected: code=%d raw=%s", code, raw)
	}
}

func lifecycleJSON(t *testing.T, yaml string) []byte {
	t.Helper()
	return lifecycleJSONSchema(t, 0, yaml)
}

func lifecycleJSONSchema(t *testing.T, schema uint32, yaml string) []byte {
	t.Helper()
	raw, err := json.Marshal(lifecycleRequest{SchemaVersion: schema, ConfigYAML: []byte(yaml)})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func managementRequestJSON(t *testing.T, method, path string, body map[string]any, hostCallbackID string) []byte {
	t.Helper()
	var rawBody []byte
	if body != nil {
		var err error
		rawBody, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(management.Request{HostCallbackID: hostCallbackID, Method: method, Path: path, Body: rawBody})
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

type fakeABICaller struct {
	t              *testing.T
	hostCallbackID string
	calls          map[string]int
}

func (c *fakeABICaller) Call(_ context.Context, method string, request []byte) ([]byte, error) {
	if c.calls == nil {
		c.calls = map[string]int{}
	}
	c.calls[method]++
	var req map[string]any
	if err := json.Unmarshal(request, &req); err != nil {
		c.t.Fatalf("callback request JSON for %s: %v", method, err)
	}
	if req["host_callback_id"] != c.hostCallbackID {
		c.t.Fatalf("callback host id mismatch for %s", method)
	}
	switch method {
	case abi.MethodAuthList:
		return hostOK(map[string]any{"files": []abi.HostAuthFileEntry{{AuthIndex: "fake-auth", Provider: "codex"}}}), nil
	case abi.MethodAuthGet:
		if req["auth_index"] != "fake-auth" {
			c.t.Fatalf("auth index mismatch")
		}
		return hostOK(map[string]any{"auth_index": "fake-auth", "name": "fake", "path": "redacted", "json": map[string]string{"access_token": "fake-access-token", "account_id": "fake-account"}}), nil
	case abi.MethodHTTPDo:
		headers, _ := req["headers"].(map[string]any)
		authValues, _ := headers["Authorization"].([]any)
		if len(authValues) != 1 || !strings.HasPrefix(authValues[0].(string), "Bearer ") {
			c.t.Fatalf("authorization header shape mismatch")
		}
		return hostOK(abi.HTTPResponse{StatusCode: 200, Body: []byte(`{"plan_type":"plus","rate_limit":{"secondary":{"used_percent":25,"limit_window_seconds":604800}}}`)}), nil
	default:
		c.t.Fatalf("unexpected callback method %s", method)
		return nil, nil
	}
}

func hostOK(result any) []byte {
	raw, err := json.Marshal(result)
	if err != nil {
		panic(err)
	}
	env, err := json.Marshal(abi.Envelope{OK: true, Result: raw})
	if err != nil {
		panic(err)
	}
	return env
}

type blockingListCaller struct {
	entered     chan struct{}
	canceled    chan struct{}
	allowReturn chan struct{}
	once        sync.Once
	getCalls    int
	httpCalls   int
}

func (c *blockingListCaller) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	switch method {
	case abi.MethodAuthList:
		c.once.Do(func() { close(c.entered) })
		<-ctx.Done()
		close(c.canceled)
		<-c.allowReturn
		return nil, ctx.Err()
	case abi.MethodAuthGet:
		c.getCalls++
		return nil, nil
	case abi.MethodHTTPDo:
		c.httpCalls++
		return nil, nil
	default:
		return nil, nil
	}
}

type reentrantShutdownCaller struct {
	app       *App
	started   chan struct{}
	returned  chan struct{}
	once      sync.Once
	getCalls  int
	httpCalls int
}

func (c *reentrantShutdownCaller) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	switch method {
	case abi.MethodAuthList:
		c.once.Do(func() { close(c.started) })
		c.app.Shutdown()
		close(c.returned)
		return nil, ctx.Err()
	case abi.MethodAuthGet:
		c.getCalls++
		return nil, nil
	case abi.MethodHTTPDo:
		c.httpCalls++
		return nil, nil
	default:
		return nil, nil
	}
}
