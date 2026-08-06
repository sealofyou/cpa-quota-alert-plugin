package codexquota

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/abi"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/config"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/quota"
)

type fakeHost struct {
	list []abi.HostAuthFileEntry
	get  map[string]fakeGet
	http map[string][]fakeHTTP

	mu        sync.Mutex
	requests  []abi.HTTPRequest
	active    int
	maxActive int
}

type fakeGet struct {
	body json.RawMessage
	err  error
}

type fakeHTTP struct {
	resp abi.HTTPResponse
	err  error
}

func (h *fakeHost) ListAuth(ctx context.Context) ([]abi.HostAuthFileEntry, error) {
	return append([]abi.HostAuthFileEntry(nil), h.list...), ctx.Err()
}

func (h *fakeHost) GetAuth(ctx context.Context, index string) (abi.AuthFile, error) {
	if err := ctx.Err(); err != nil {
		return abi.AuthFile{}, err
	}
	g, ok := h.get[index]
	if !ok {
		return abi.AuthFile{}, errors.New("missing fake auth")
	}
	if g.err != nil {
		return abi.AuthFile{}, g.err
	}
	return abi.AuthFile{AuthIndex: index, JSON: append([]byte(nil), g.body...)}, nil
}

func (h *fakeHost) HTTPDo(ctx context.Context, req abi.HTTPRequest) (abi.HTTPResponse, error) {
	if err := ctx.Err(); err != nil {
		return abi.HTTPResponse{}, err
	}
	h.mu.Lock()
	h.active++
	if h.active > h.maxActive {
		h.maxActive = h.active
	}
	h.requests = append(h.requests, req)
	seq := h.http[req.Headers["Chatgpt-Account-Id"][0]]
	if len(seq) == 0 {
		seq = h.http["*"]
	}
	var item fakeHTTP
	if len(seq) > 0 {
		item = seq[0]
		h.http[req.Headers["Chatgpt-Account-Id"][0]] = seq[1:]
	}
	h.mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	h.mu.Lock()
	h.active--
	h.mu.Unlock()
	return item.resp, item.err
}

func testConfig() config.Config {
	return config.Config{
		Concurrency:           2,
		RequestTimeoutSeconds: 1,
		RetryAttempts:         2,
		QuotaURL:              config.DefaultQuotaURL,
		PlanRules:             map[string]config.PlanRule{"plus": {Name: "plus", Aliases: []string{"plus"}, Window: config.Window7d, Weight: 1}},
		IgnoredPlans:          config.PlanSet{"free": {}},
		TerminalErrorCodes:    config.StringSet{"token_invalidated": {}, "forbidden": {}},
	}
}

func authJSON(token, account string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"access_token":%q,"account_id":%q}`, token, account))
}

func quotaBody(plan string, used int) []byte {
	return []byte(fmt.Sprintf(`{"plan_type":%q,"rate_limit":{"secondary":{"used_percent":%d,"limit_window_seconds":604800}}}`, plan, used))
}

func TestCheckFiltersAndKeepsStableOrderWithPartialErrors(t *testing.T) {
	h := &fakeHost{
		list: []abi.HostAuthFileEntry{
			{AuthIndex: "skip", Provider: "github"},
			{AuthIndex: "disabled", Provider: "codex", Disabled: true},
			{AuthIndex: "ok1", Provider: "CODEX"},
			{Provider: "codex"},
			{AuthIndex: "missing", Type: "codex"},
			{AuthIndex: "runtime", Provider: "codex", RuntimeOnly: true},
			{AuthIndex: "ok2", Type: "CoDeX", Unavailable: true},
		},
		get: map[string]fakeGet{
			"ok1": {body: authJSON("tok-one", "acct-one")},
			"ok2": {body: authJSON("tok-two", "acct-two")},
		},
		http: map[string][]fakeHTTP{
			"acct-one": {{resp: abi.HTTPResponse{StatusCode: 200, Body: quotaBody("plus", 25)}}},
			"acct-two": {{resp: abi.HTTPResponse{StatusCode: 200, Body: []byte(`{"planType":"plus","rateLimit":{"secondary":{"usedPercent":50,"limitWindowSeconds":604800}}}`)}}},
		},
	}
	obs, stats := NewService(h, testConfig()).Check(context.Background())
	if stats.Selected != 5 || stats.Skipped != 1 || stats.Disabled != 1 || stats.RuntimeOnly != 1 || stats.GetFailed != 1 || stats.MissingIndex != 1 || stats.Unavailable != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if len(obs) != 5 {
		t.Fatalf("observations len=%d %+v", len(obs), obs)
	}
	if !obs[0].Success || obs[0].WeightedRemaining != 0.75 || obs[1].UnresolvedCode != "missingauthindex" || obs[2].UnresolvedCode != "authgeterror" || obs[3].UnresolvedCode != "runtimeonly" || !obs[4].Success || obs[4].WeightedRemaining != 0.5 {
		t.Fatalf("unexpected stable observations: %+v", obs)
	}
}

func TestParseCredentialDirectAndJWTClaims(t *testing.T) {
	direct, err := ParseCredential([]byte(`{"access_token":"tok","chatgpt_account_id":"acct-direct"}`))
	if err != nil || direct.AccessToken != "tok" || direct.AccountID != "acct-direct" {
		t.Fatalf("direct=%+v err=%v", direct, err)
	}
	payload, _ := json.Marshal(map[string]string{"https://api.openai.com/auth.chatgpt_account_id": "acct-jwt"})
	jwt := "x." + base64.RawURLEncoding.EncodeToString(payload) + ".y"
	fromJWT, err := ParseCredential([]byte(fmt.Sprintf(`{"access_token":"tok","id_token":%q}`, jwt)))
	if err != nil || fromJWT.AccountID != "acct-jwt" {
		t.Fatalf("jwt=%+v err=%v", fromJWT, err)
	}
	for _, body := range [][]byte{[]byte(`[]`), []byte(`{"access_token":"tok","id_token":"bad"}`), []byte(`{"account_id":"acct"}`)} {
		if _, err := ParseCredential(body); err == nil {
			t.Fatalf("expected error for %s", body)
		}
	}
}

func TestHTTPHeadersRedactedErrorsAndRetryPolicy(t *testing.T) {
	retryable := &abi.CallbackError{Code: "temporary", Retryable: true}
	h := &fakeHost{
		list: []abi.HostAuthFileEntry{{AuthIndex: "a", Provider: "codex"}, {AuthIndex: "b", Provider: "codex"}, {AuthIndex: "c", Provider: "codex"}, {AuthIndex: "d", Provider: "codex"}, {AuthIndex: "e", Provider: "codex"}},
		get:  map[string]fakeGet{"a": {body: authJSON("secret-token", "acct-a")}, "b": {body: authJSON("tok-b", "acct-b")}, "c": {body: authJSON("tok-c", "acct-c")}, "d": {body: authJSON("tok-d", "acct-d")}, "e": {body: authJSON("tok-e", "acct-e")}},
		http: map[string][]fakeHTTP{
			"acct-a": {{resp: abi.HTTPResponse{StatusCode: 401, Body: []byte(`{"error":{"code":"token_invalidated","message":"secret-token acct-a"}}`)}}},
			"acct-b": {{resp: abi.HTTPResponse{StatusCode: 429, Body: []byte(`too many`)}}},
			"acct-c": {{resp: abi.HTTPResponse{StatusCode: 500, Body: []byte(`{"detail":{"code":"upstream_down"}}`)}}, {resp: abi.HTTPResponse{StatusCode: 200, Body: quotaBody("plus", 10)}}},
			"acct-d": {{err: retryable}, {resp: abi.HTTPResponse{StatusCode: 200, Body: quotaBody("plus", 20)}}},
			"acct-e": {{resp: abi.HTTPResponse{StatusCode: 403, Body: []byte(`forbidden`)}}},
		},
	}
	obs, _ := NewService(h, testConfig()).Check(context.Background())
	if obs[0].TerminalCode != "tokeninvalidated" || obs[1].UnresolvedCode != "http429" || !obs[2].Success || !obs[3].Success || obs[4].UnresolvedCode != "http403" {
		t.Fatalf("unexpected observations: %+v", obs)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.requests) != 7 {
		t.Fatalf("expected 7 requests with retries, got %d", len(h.requests))
	}
	var found bool
	for _, req := range h.requests {
		if req.Headers["Chatgpt-Account-Id"][0] != "acct-a" {
			continue
		}
		found = true
		if req.Method != "GET" || req.URL != config.DefaultQuotaURL || req.Headers["Authorization"][0] != "Bearer secret-token" || req.Headers["Accept"][0] != "application/json" || req.Headers["User-Agent"][0] != "cpa-quota-alert-plugin/0.1" {
			t.Fatalf("headers did not match expected shape")
		}
	}
	if !found {
		t.Fatalf("expected request for selected account")
	}
	if strings.Contains(fmt.Sprint(obs), "secret-token") || strings.Contains(fmt.Sprint(obs), "acct-a") {
		t.Fatalf("observations leaked secret/account: %+v", obs)
	}
}

func TestConcurrencyLimitAndCancellation(t *testing.T) {
	h := &fakeHost{get: map[string]fakeGet{}, http: map[string][]fakeHTTP{}}
	for i := 0; i < 8; i++ {
		idx := fmt.Sprintf("idx-%d", i)
		acct := fmt.Sprintf("acct-%d", i)
		h.list = append(h.list, abi.HostAuthFileEntry{AuthIndex: idx, Provider: "codex"})
		h.get[idx] = fakeGet{body: authJSON("tok", acct)}
		h.http[acct] = []fakeHTTP{{resp: abi.HTTPResponse{StatusCode: 200, Body: quotaBody("plus", i)}}}
	}
	cfg := testConfig()
	cfg.Concurrency = 3
	obs, _ := NewService(h, cfg).Check(context.Background())
	if len(obs) != 8 || h.maxActive > 3 {
		t.Fatalf("len=%d maxActive=%d", len(obs), h.maxActive)
	}
	for i, ob := range obs {
		if !ob.Success || ob.WeightedRemaining <= 0 {
			t.Fatalf("obs[%d]=%+v", i, ob)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	obs, _ = NewService(h, cfg).Check(ctx)
	if len(obs) != 0 {
		t.Fatalf("canceled before dispatch should return no observations, got %+v", obs)
	}
}

func TestInvalidJSONAndAllFailuresStillReturnObservations(t *testing.T) {
	h := &fakeHost{list: []abi.HostAuthFileEntry{{AuthIndex: "bad-json", Provider: "codex"}, {AuthIndex: "host", Provider: "codex"}}, get: map[string]fakeGet{"bad-json": {body: authJSON("tok", "acct")}, "host": {err: errors.New("path C:/secret email@example.com")}}, http: map[string][]fakeHTTP{"acct": {{resp: abi.HTTPResponse{StatusCode: 200, Body: []byte(`not-json`)}}}}}
	obs, _ := NewService(h, testConfig()).Check(context.Background())
	if len(obs) != 2 || obs[0].UnresolvedCode != "parse_error" || obs[1].UnresolvedCode != "authgeterror" {
		t.Fatalf("unexpected observations: %+v", obs)
	}
	if _, err := quota.AggregateObservations(obs, time.Now()); !errors.Is(err, quota.ErrNotComputable) {
		t.Fatalf("aggregate should see all failures from returned observations, got %v", err)
	}
}
