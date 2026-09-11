package management

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/codexquota"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/config"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/monitor"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/notify"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/quota"
)

func TestRegisterReturnsProtectedRoutesAndEmptyResources(t *testing.T) {
	h := newTestHandler(t)
	raw, code := h.Call(context.Background(), MethodManagementRegister, nil)
	if code != 0 {
		t.Fatalf("register code=%d raw=%s", code, raw)
	}
	var got Registration
	decodeJSON(t, raw, &got)
	if len(got.Resources) != 0 {
		t.Fatalf("resources must be empty: %+v", got.Resources)
	}
	want := []Route{
		{Method: "POST", Path: "/cpa-quota-alert/check", Description: "Run a CPA quota check."},
		{Method: "GET", Path: "/cpa-quota-alert/status", Description: "Read CPA quota alert status."},
		{Method: "POST", Path: "/cpa-quota-alert/test-notification", Description: "Send a CPA quota alert test notification."},
	}
	if len(got.Routes) != len(want) {
		t.Fatalf("routes=%+v", got.Routes)
	}
	for i := range want {
		if got.Routes[i] != want[i] {
			t.Fatalf("route[%d]=%+v want %+v", i, got.Routes[i], want[i])
		}
	}
}

func TestManagementWireFormatUsesOfficialKeysAndBase64Bodies(t *testing.T) {
	h := newTestHandler(t)
	raw, _ := h.Call(context.Background(), MethodManagementRegister, nil)
	var registration map[string]any
	decodeJSON(t, raw, &registration)
	if _, ok := registration["protected_routes"]; ok {
		t.Fatalf("registration must not expose protected_routes: %s", raw)
	}
	if keys := sortedKeys(registration); !reflect.DeepEqual(keys, []string{"resources", "routes"}) {
		t.Fatalf("registration keys=%v raw=%s", keys, raw)
	}
	route := registration["routes"].([]any)[0].(map[string]any)
	if keys := sortedKeys(route); !reflect.DeepEqual(keys, []string{"Description", "Method", "Path"}) {
		t.Fatalf("route keys=%v route=%+v", keys, route)
	}

	req := managementRequest(t, "POST", "/cpa-quota-alert/check", map[string]any{"dry_run": true})
	var envelope map[string]any
	decodeJSON(t, req, &envelope)
	encodedBody, ok := envelope["Body"].(string)
	if !ok {
		t.Fatalf("request Body must be base64 string: %s", req)
	}
	decoded, err := base64.StdEncoding.DecodeString(encodedBody)
	if err != nil {
		t.Fatalf("request Body is not base64: %v", err)
	}
	if string(decoded) != `{"dry_run":true}` {
		t.Fatalf("decoded Body=%s", decoded)
	}

	resp := decodeResponse(t, mustCall(t, h, "POST", "/cpa-quota-alert/check", map[string]any{"dry_run": true}))
	responseWire := marshalResponse(resp)
	decodeJSON(t, responseWire, &envelope)
	encodedBody, ok = envelope["Body"].(string)
	if !ok {
		t.Fatalf("response Body must be base64 string: %s", responseWire)
	}
	decoded, err = base64.StdEncoding.DecodeString(encodedBody)
	if err != nil {
		t.Fatalf("response Body is not base64: %v", err)
	}
	if !json.Valid(decoded) {
		t.Fatalf("decoded response Body is not JSON: %s", decoded)
	}
}

func TestNormalCheckSavesPendingBeforeSendingAndReturnsAggregate(t *testing.T) {
	h := newTestHandler(t)
	env := h.deps.StoreFactory.(*fakeStoreFactory).store
	raw, _ := h.Call(context.Background(), MethodManagementHandle, managementRequest(t, "POST", "/v0/management/cpa-quota-alert/check", nil))
	resp := decodeResponse(t, raw)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var body map[string]any
	decodeJSON(t, resp.Body, &body)
	if body["computable"] != true || body["total"] != 1.0 || body["selected"] != 1.0 || body["partial"] != false {
		t.Fatalf("unexpected aggregate: %+v", body)
	}
	if env.saveCount < 2 || len(env.snapshots[0].PendingEvents) != 1 {
		t.Fatalf("expected pre-send save with pending event: saves=%d snapshots=%+v", env.saveCount, env.snapshots)
	}
	if got := h.sender("smtp").sentCount(); got != 1 {
		t.Fatalf("smtp sends=%d", got)
	}
	sent := h.sender("smtp").last()
	if sent.Subject != "[CPA quota] remaining 1.00 Plus-week equivalents" {
		t.Fatalf("templated subject=%q", sent.Subject)
	}
	if strings.Contains(sent.Body, "@") || strings.Contains(strings.ToLower(sent.Body), "vps") {
		t.Fatalf("mail leaked operator-specific text: %q", sent.Body)
	}
	if got := h.sender("webhook").sentCount(); got != 1 {
		t.Fatalf("webhook sends=%d", got)
	}
	if len(env.state.PendingEvents) != 0 || len(env.state.DeliveredEvents) != 1 {
		t.Fatalf("expected all delivered: %+v", env.state)
	}
}

func TestFailedChannelIsRetriedWithoutDuplicatingDeliveredChannel(t *testing.T) {
	h := newTestHandler(t)
	h.sender("webhook").err = notify.Error{Code: "webhook_status_failed"}
	first := decodeResponse(t, mustCall(t, h, "POST", "/cpa-quota-alert/check", nil))
	if first.StatusCode != 200 {
		t.Fatalf("first status=%d body=%s", first.StatusCode, first.Body)
	}
	if h.sender("smtp").sentCount() != 1 || h.sender("webhook").sentCount() != 1 {
		t.Fatalf("first sends smtp=%d webhook=%d", h.sender("smtp").sentCount(), h.sender("webhook").sentCount())
	}
	h.sender("webhook").err = nil
	second := decodeResponse(t, mustCall(t, h, "POST", "/cpa-quota-alert/check", nil))
	if second.StatusCode != 200 {
		t.Fatalf("second status=%d body=%s", second.StatusCode, second.Body)
	}
	if h.sender("smtp").sentCount() != 1 {
		t.Fatalf("smtp delivered channel was duplicated")
	}
	if h.sender("webhook").sentCount() != 2 {
		t.Fatalf("webhook should retry once, got %d", h.sender("webhook").sentCount())
	}
}

func TestDryRunDoesNotSendOrMarkDeliveredButPersistsPending(t *testing.T) {
	h := newTestHandler(t)
	resp := decodeResponse(t, mustCall(t, h, "POST", "/cpa-quota-alert/check", map[string]any{"dry_run": true}))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var body map[string]any
	decodeJSON(t, resp.Body, &body)
	if body["would_notify"] != true {
		t.Fatalf("expected would_notify: %+v", body)
	}
	if h.sender("smtp").sentCount() != 0 || h.sender("webhook").sentCount() != 0 {
		t.Fatalf("dry-run sent notifications")
	}
	state := h.deps.StoreFactory.(*fakeStoreFactory).store.state
	if len(state.PendingEvents) != 1 || state.PendingEvents[0].Delivery["smtp"] != "pending" {
		t.Fatalf("pending not retained: %+v", state)
	}
}

func TestStorePreSendFailurePreventsAllSends(t *testing.T) {
	h := newTestHandler(t)
	store := h.deps.StoreFactory.(*fakeStoreFactory).store
	store.saveErrAt = 1
	resp := decodeResponse(t, mustCall(t, h, "POST", "/cpa-quota-alert/check", nil))
	if resp.StatusCode != 500 || !strings.Contains(string(resp.Body), "store_save_failed") {
		t.Fatalf("response=%+v", resp)
	}
	if h.sender("smtp").sentCount() != 0 || h.sender("webhook").sentCount() != 0 {
		t.Fatalf("sent before durable pending save")
	}
}

func TestTryLockRejectsConcurrentCheck(t *testing.T) {
	h := newTestHandler(t)
	checker := h.deps.CheckerFactory.(*fakeCheckerFactory).checker
	checker.block = make(chan struct{})
	checker.blocked = make(chan struct{})
	done := make(chan struct{})
	go func() {
		_ = mustCall(t, h, "POST", "/cpa-quota-alert/check", nil)
		close(done)
	}()
	checker.waitUntilBlocked(t)
	resp := decodeResponse(t, mustCall(t, h, "POST", "/cpa-quota-alert/check", nil))
	if resp.StatusCode != 409 || !strings.Contains(string(resp.Body), "check_in_progress") {
		t.Fatalf("response=%+v", resp)
	}
	close(checker.block)
	<-done
}

func TestStatusStaleRecipientCountAndRedaction(t *testing.T) {
	h := newTestHandler(t)
	store := h.deps.StoreFactory.(*fakeStoreFactory).store
	store.state = monitor.State{
		LowActive:           true,
		ConsecutiveFailures: 2,
		LastValidTotal:      1.2,
		LastValidAt:         h.deps.Clock.(fakeClock).now.Add(-time.Hour),
		PendingEvents: []monitor.Event{{
			ID: "evt-1", Kind: monitor.EventLow, Delivery: map[string]string{
				"smtp": "pending", "webhook": "failed",
			},
		}},
	}
	resp := decodeResponse(t, mustCall(t, h, "GET", "/cpa-quota-alert/status", nil))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	text := string(resp.Body)
	for _, secret := range []string{"alerts@example.com", "https://hook.example/private", "secret", "token", "acct_"} {
		if strings.Contains(text, secret) {
			t.Fatalf("status leaked %q: %s", secret, text)
		}
	}
	var body map[string]any
	decodeJSON(t, resp.Body, &body)
	if body["stale"] != true || body["configured"] != true {
		t.Fatalf("unexpected status: %+v", body)
	}
	channels := body["channels"].([]any)
	if channels[0].(map[string]any)["recipient_count"] != float64(2) {
		t.Fatalf("recipient count not exposed safely: %+v", channels)
	}

	store.state.LastValidAt = h.deps.Clock.(fakeClock).now.Add(time.Minute)
	resp = decodeResponse(t, mustCall(t, h, "GET", "/cpa-quota-alert/status", nil))
	decodeJSON(t, resp.Body, &body)
	if body["stale"] != true {
		t.Fatalf("future last_valid_at must be stale: %+v", body)
	}
}

func TestTestNotificationDoesNotModifyStateAndDryRunSkipsSending(t *testing.T) {
	h := newTestHandler(t)
	store := h.deps.StoreFactory.(*fakeStoreFactory).store
	store.state = monitor.State{LowActive: true}
	resp := decodeResponse(t, mustCall(t, h, "POST", "/cpa-quota-alert/test-notification", map[string]any{"dry_run": true}))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	if h.sender("smtp").sentCount() != 0 || store.saveCount != 0 || !store.state.LowActive {
		t.Fatalf("test notification changed state or sent: state=%+v saves=%d", store.state, store.saveCount)
	}
}

func TestUnknownPlanAndNotComputableAggregatesAreObservableHTTP200(t *testing.T) {
	h := newTestHandler(t)
	h.deps.CheckerFactory.(*fakeCheckerFactory).checker.observations = []quota.AccountObservation{{Plan: "enterprise", UnknownPlan: "enterprise"}}
	resp := decodeResponse(t, mustCall(t, h, "POST", "/cpa-quota-alert/check", nil))
	if resp.StatusCode != 200 {
		t.Fatalf("unknown plan status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var body map[string]any
	decodeJSON(t, resp.Body, &body)
	if body["error_code"] != "plan_changed" || body["computable"] != false {
		t.Fatalf("unexpected unknown plan aggregate: %+v", body)
	}
	if unknown := body["unknown_plan_counts"].(map[string]any); unknown["enterprise"] != float64(1) {
		t.Fatalf("unknown plan counts: %+v", unknown)
	}

	h = newTestHandler(t)
	h.deps.CheckerFactory.(*fakeCheckerFactory).checker.observations = []quota.AccountObservation{{UnresolvedCode: "missing_index", Partial: true}}
	for i := 0; i < 3; i++ {
		resp = decodeResponse(t, mustCall(t, h, "POST", "/cpa-quota-alert/check", nil))
	}
	if resp.StatusCode != 200 {
		t.Fatalf("not computable status=%d body=%s", resp.StatusCode, resp.Body)
	}
	decodeJSON(t, resp.Body, &body)
	if body["error_code"] != "not_computable" || body["computable"] != false {
		t.Fatalf("unexpected not computable aggregate: %+v", body)
	}
	state := h.deps.StoreFactory.(*fakeStoreFactory).store.state
	if len(state.DeliveredEvents) != 1 {
		t.Fatalf("expected data_error after three failures: %+v", state)
	}
	for _, event := range state.DeliveredEvents {
		if event.Kind != monitor.EventDataError {
			t.Fatalf("expected data_error after three failures: %+v", state)
		}
	}
}

func TestRouteAndRequestErrorsAreStable(t *testing.T) {
	h := newTestHandler(t)
	tests := []struct {
		method string
		path   string
		body   []byte
		status int
	}{
		{"GET", "/cpa-quota-alert/check", nil, 405},
		{"POST", "/cpa-quota-alert/missing", nil, 404},
		{"POST", "/cpa-quota-alert/check", []byte(`{"extra":true}`), 400},
		{"POST", "/cpa-quota-alert/check", []byte(`{`), 400},
	}
	for _, tt := range tests {
		rawBody := tt.body
		var req []byte
		if rawBody == nil {
			req = managementRequest(t, tt.method, tt.path, nil)
		} else if string(rawBody) == "{" {
			req = []byte(`{"Method":"` + tt.method + `","Path":"` + tt.path + `","Body":{`)
		} else {
			req = managementRequestRawBody(t, tt.method, tt.path, rawBody)
		}
		raw, _ := h.Call(context.Background(), MethodManagementHandle, req)
		resp := decodeResponse(t, raw)
		if resp.StatusCode != tt.status {
			t.Fatalf("%s %s got %d body=%s", tt.method, tt.path, resp.StatusCode, resp.Body)
		}
	}
	huge := strings.Repeat("x", MaxRequestBodyBytes+1)
	resp := decodeResponse(t, mustCallRawBody(t, h, "POST", "/cpa-quota-alert/check", []byte(`{"dry_run":"`+huge+`"}`)))
	if resp.StatusCode != 413 {
		t.Fatalf("huge body status=%d", resp.StatusCode)
	}
	resp = decodeResponse(t, rawCall(t, h, []byte(`{"Method":"POST","Path":"/cpa-quota-alert/check","Headers":{"x":["`+strings.Repeat("h", MaxManagementRequestBytes)+`"]}}`)))
	if resp.StatusCode != 413 {
		t.Fatalf("huge header envelope status=%d", resp.StatusCode)
	}
	resp = decodeResponse(t, rawCall(t, h, []byte(`{"Method":"POST","Path":"/cpa-quota-alert/check","Query":{"x":["`+strings.Repeat("q", MaxManagementRequestBytes)+`"]}}`)))
	if resp.StatusCode != 413 {
		t.Fatalf("huge query envelope status=%d", resp.StatusCode)
	}
}

func TestDeliveryErrorCodeRejectsUnstableCodes(t *testing.T) {
	h := newTestHandler(t)
	secret := "topsecret_should_not_leak\r\nnext value"
	h.sender("webhook").err = notify.Error{Code: "BAD_" + secret}
	resp := decodeResponse(t, mustCall(t, h, "POST", "/cpa-quota-alert/check", nil))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	text := string(resp.Body)
	if strings.Contains(text, secret) || strings.Contains(text, "BAD_") {
		t.Fatalf("unstable error code leaked: %q", text)
	}
	if !strings.Contains(text, "delivery_failed") {
		t.Fatalf("missing fallback error code: %q", text)
	}

	h = newTestHandler(t)
	shapedSecret := "topsecret_should_not_leak"
	h.sender("webhook").err = notify.Error{Code: shapedSecret}
	resp = decodeResponse(t, mustCall(t, h, "POST", "/cpa-quota-alert/check", nil))
	if resp.StatusCode != 200 {
		t.Fatalf("check status=%d body=%s", resp.StatusCode, resp.Body)
	}
	text = string(resp.Body)
	if strings.Contains(text, shapedSecret) {
		t.Fatalf("allowlist bypass leaked from check: %q", text)
	}
	if !strings.Contains(text, "delivery_failed") {
		t.Fatalf("missing fallback error code from check: %q", text)
	}

	h = newTestHandler(t)
	h.sender("webhook").err = notify.Error{Code: shapedSecret}
	resp = decodeResponse(t, mustCall(t, h, "POST", "/cpa-quota-alert/test-notification", nil))
	if resp.StatusCode != 200 {
		t.Fatalf("test-notification status=%d body=%s", resp.StatusCode, resp.Body)
	}
	text = string(resp.Body)
	if strings.Contains(text, shapedSecret) {
		t.Fatalf("allowlist bypass leaked from test-notification: %q", text)
	}
	if !strings.Contains(text, "delivery_failed") {
		t.Fatalf("missing fallback error code from test-notification: %q", text)
	}
}

func TestConfiguredChannelsAreDeduplicatedByNormalizedName(t *testing.T) {
	h := newTestHandler(t)
	firstSMTP := &fakeSender{}
	secondSMTP := &fakeSender{}
	firstWebhook := &fakeSender{}
	secondWebhook := &fakeSender{}
	h.deps.ChannelFactory.(*fakeChannelFactory).channels = []Channel{
		{Name: " SMTP ", Sender: firstSMTP, RecipientCount: 2},
		{Name: "smtp", Sender: secondSMTP, RecipientCount: 99},
		{Name: "WEBHOOK", Sender: firstWebhook},
		{Name: "webhook", Sender: secondWebhook},
	}
	resp := decodeResponse(t, mustCall(t, h, "POST", "/cpa-quota-alert/check", nil))
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	if firstSMTP.sentCount() != 1 || secondSMTP.sentCount() != 0 || firstWebhook.sentCount() != 1 || secondWebhook.sentCount() != 0 {
		t.Fatalf("dedupe sends smtp=%d/%d webhook=%d/%d", firstSMTP.sentCount(), secondSMTP.sentCount(), firstWebhook.sentCount(), secondWebhook.sentCount())
	}
	resp = decodeResponse(t, mustCall(t, h, "GET", "/cpa-quota-alert/status", nil))
	var body map[string]any
	decodeJSON(t, resp.Body, &body)
	channels := body["channels"].([]any)
	if len(channels) != 2 {
		t.Fatalf("dedupe status channels=%+v", channels)
	}
	if channels[0].(map[string]any)["name"] != "smtp" || channels[0].(map[string]any)["recipient_count"] != float64(2) || channels[1].(map[string]any)["name"] != "webhook" {
		t.Fatalf("dedupe status channels=%+v", channels)
	}
}

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	cfg := testConfig()
	smtp := &fakeSender{}
	webhook := &fakeSender{}
	deps := Dependencies{
		ConfigProvider: configProviderFunc(func(context.Context) (config.Config, bool) { return cfg, true }),
		CheckerFactory: &fakeCheckerFactory{checker: &fakeChecker{
			observations: []quota.AccountObservation{{
				Plan: "plus", CanonicalPlan: "plus", WeightedRemaining: 1.0, Success: true, Window: quota.Window7d,
			}},
			stats: codexquota.DiscoveryStats{Selected: 1},
		}},
		StoreFactory: &fakeStoreFactory{store: &fakeStore{}},
		ChannelFactory: &fakeChannelFactory{channels: []Channel{
			{Name: "smtp", Sender: smtp, RecipientCount: 2},
			{Name: "webhook", Sender: webhook},
		}},
		Clock: fakeClock{now: time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)},
	}
	h, err := NewHandler(deps)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func testConfig() config.Config {
	return config.Config{
		StaleAfterSeconds:     900,
		LowThreshold:          1.5,
		RecoveryThreshold:     1.6,
		ReminderSeconds:       86400,
		FailureAlertCount:     3,
		PlanRules:             map[string]config.PlanRule{"plus": {Name: "plus", Aliases: []string{"plus"}, Window: quota.Window7d, Weight: 1}},
		IgnoredPlans:          config.PlanSet{"free": {}},
		TerminalErrorCodes:    config.StringSet{},
		SMTP:                  config.SMTPConfig{Enabled: true, RecipientsEnv: "SMTP_RECIPIENTS"},
		Webhook:               config.WebhookConfig{Enabled: true, URLEnv: "WEBHOOK_URL", AuthHeaderEnv: "WEBHOOK_AUTH"},
		RequestTimeoutSeconds: 1,
	}
}

func (h *Handler) sender(name string) *fakeSender {
	for _, ch := range h.deps.ChannelFactory.(*fakeChannelFactory).channels {
		if ch.Name == name {
			return ch.Sender.(*fakeSender)
		}
	}
	return nil
}

func mustCall(t *testing.T, h *Handler, method, path string, body map[string]any) []byte {
	t.Helper()
	raw, code := h.Call(context.Background(), MethodManagementHandle, managementRequest(t, method, path, body))
	if code != 0 {
		t.Fatalf("call code=%d raw=%s", code, raw)
	}
	return raw
}

func mustCallRawBody(t *testing.T, h *Handler, method, path string, body []byte) []byte {
	t.Helper()
	raw, code := h.Call(context.Background(), MethodManagementHandle, managementRequestRawBody(t, method, path, body))
	if code != 0 {
		t.Fatalf("call code=%d raw=%s", code, raw)
	}
	return raw
}

func managementRequest(t *testing.T, method, path string, body map[string]any) []byte {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	return managementRequestRawBody(t, method, path, raw)
}

func managementRequestRawBody(t *testing.T, method, path string, body []byte) []byte {
	t.Helper()
	req := Request{HostCallbackID: "host-1", Method: method, Path: path, Headers: map[string][]string{}, Query: map[string][]string{}, Body: body}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func rawCall(t *testing.T, h *Handler, req []byte) []byte {
	t.Helper()
	raw, code := h.Call(context.Background(), MethodManagementHandle, req)
	if code != 0 {
		t.Fatalf("call code=%d raw=%s", code, raw)
	}
	return raw
}

func decodeResponse(t *testing.T, raw []byte) Response {
	t.Helper()
	var resp Response
	decodeJSON(t, raw, &resp)
	return resp
}

func decodeJSON(t *testing.T, raw []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}

func sortedKeys(in map[string]any) []string {
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type configProviderFunc func(context.Context) (config.Config, bool)

func (f configProviderFunc) Current(ctx context.Context) (config.Config, bool) { return f(ctx) }

type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }

type fakeCheckerFactory struct{ checker *fakeChecker }

func (f *fakeCheckerFactory) NewChecker(string, config.Config) (Checker, error) {
	return f.checker, nil
}

type fakeChecker struct {
	observations []quota.AccountObservation
	stats        codexquota.DiscoveryStats
	block        chan struct{}
	blockedOnce  sync.Once
	blocked      chan struct{}
}

func (c *fakeChecker) Check(context.Context) ([]quota.AccountObservation, codexquota.DiscoveryStats) {
	if c.block != nil {
		if c.blocked == nil {
			c.blocked = make(chan struct{})
		}
		c.blockedOnce.Do(func() { close(c.blocked) })
		<-c.block
	}
	return append([]quota.AccountObservation(nil), c.observations...), c.stats
}

func (c *fakeChecker) waitUntilBlocked(t *testing.T) {
	t.Helper()
	select {
	case <-c.blocked:
	case <-time.After(time.Second):
		t.Fatal("checker did not block")
	}
}

type fakeStoreFactory struct{ store *fakeStore }

func (f *fakeStoreFactory) NewStore(config.Config) (Store, error) { return f.store, nil }

type fakeStore struct {
	state     monitor.State
	saveCount int
	saveErrAt int
	snapshots []monitor.State
}

func (s *fakeStore) Load(context.Context) (monitor.State, error) { return s.state, nil }

func (s *fakeStore) Save(_ context.Context, state monitor.State) error {
	s.saveCount++
	if s.saveErrAt == s.saveCount {
		return errors.New("disk contains secret should not leak")
	}
	s.state = state
	s.snapshots = append(s.snapshots, state)
	return nil
}

type fakeChannelFactory struct{ channels []Channel }

func (f *fakeChannelFactory) Channels(config.Config) ([]Channel, error) {
	return append([]Channel(nil), f.channels...), nil
}

type fakeSender struct {
	mu   sync.Mutex
	sent []notify.Message
	err  error
}

func (s *fakeSender) Send(_ context.Context, msg notify.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, msg)
	return s.err
}

func (s *fakeSender) sentCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func (s *fakeSender) last() notify.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent[len(s.sent)-1]
}
