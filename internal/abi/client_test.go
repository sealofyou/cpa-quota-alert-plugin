package abi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type recordingCaller struct {
	method string
	req    []byte
	resp   []byte
	err    error
}

func (c *recordingCaller) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.method = method
	c.req = append([]byte(nil), request...)
	if c.err != nil {
		return nil, c.err
	}
	return append([]byte(nil), c.resp...), nil
}

func envelope(t *testing.T, result string) []byte {
	t.Helper()
	return []byte(`{"ok":true,"result":` + result + `}`)
}

func TestClientListAuthUsesExactMethodAndKeys(t *testing.T) {
	caller := &recordingCaller{resp: envelope(t, `{"files":[{"auth_index":"idx-1","type":"oauth","provider":"codex","status":"active","disabled":false,"unavailable":true,"runtime_only":false}]}`)}
	client := NewClient(caller, "host-123")

	files, err := client.ListAuth(context.Background())
	if err != nil {
		t.Fatalf("ListAuth: %v", err)
	}
	if caller.method != MethodAuthList {
		t.Fatalf("method = %q", caller.method)
	}
	var req map[string]any
	if err := json.Unmarshal(caller.req, &req); err != nil {
		t.Fatalf("request JSON: %v", err)
	}
	if req["host_callback_id"] != "host-123" {
		t.Fatalf("host_callback_id not propagated: %s", caller.req)
	}
	if len(files) != 1 || files[0].AuthIndex != "idx-1" || !files[0].Unavailable {
		t.Fatalf("unexpected files: %+v", files)
	}
}

func TestClientGetAuthAndRuntimeStrictResults(t *testing.T) {
	caller := &recordingCaller{resp: envelope(t, `{"auth_index":"idx-1","name":"codex","path":"C:/secret/auth.json","json":{"access_token":"tok"}}`)}
	client := NewClient(caller, "")
	auth, err := client.GetAuth(context.Background(), "idx-1")
	if err != nil {
		t.Fatalf("GetAuth: %v", err)
	}
	if caller.method != MethodAuthGet {
		t.Fatalf("GetAuth method = %q", caller.method)
	}
	if !bytes.Equal(auth.JSON, []byte(`{"access_token":"tok"}`)) {
		t.Fatalf("raw auth JSON = %s", auth.JSON)
	}
	auth.JSON[0] = '['
	if bytes.Equal(auth.JSON, []byte(`[\"access_token\":\"tok\"}`)) {
		t.Fatalf("mutation sanity check failed")
	}

	caller.resp = envelope(t, `{"auth":{"auth_index":"runtime","provider":"codex","runtime_only":true}}`)
	runtime, err := client.GetRuntime(context.Background())
	if err != nil {
		t.Fatalf("GetRuntime: %v", err)
	}
	if caller.method != MethodAuthGetRuntime || runtime.Auth.AuthIndex != "runtime" || !runtime.Auth.RuntimeOnly {
		t.Fatalf("unexpected runtime: method=%q runtime=%+v", caller.method, runtime)
	}
}

func TestClientHTTPDoExactRequestAndDeepCopy(t *testing.T) {
	caller := &recordingCaller{resp: envelope(t, `{"status_code":200,"headers":{"x-one":["a","b"]},"body":"b2s="}`)}
	client := NewClient(caller, "host-123")
	headers := map[string][]string{"Authorization": []string{"Bearer secret"}}
	body := []byte("request")
	resp, err := client.HTTPDo(context.Background(), HTTPRequest{
		Method:  "GET",
		URL:     "https://chatgpt.com/backend-api/wham/usage",
		Headers: headers,
		Body:    body,
	})
	if err != nil {
		t.Fatalf("HTTPDo: %v", err)
	}
	if caller.method != MethodHTTPDo {
		t.Fatalf("method = %q", caller.method)
	}
	headers["Authorization"][0] = "mutated"
	body[0] = 'X'
	var req struct {
		HostCallbackID string              `json:"host_callback_id"`
		Method         string              `json:"method"`
		URL            string              `json:"url"`
		Headers        map[string][]string `json:"headers"`
		Body           []byte              `json:"body"`
	}
	if err := json.Unmarshal(caller.req, &req); err != nil {
		t.Fatalf("request JSON: %v", err)
	}
	if req.HostCallbackID != "host-123" || req.Headers["Authorization"][0] != "Bearer secret" || string(req.Body) != "request" {
		t.Fatalf("request was not copied/encoded correctly: %+v", req)
	}
	resp.Headers["x-one"][0] = "mutated"
	resp.Body[0] = 'X'
	resp2, err := client.HTTPDo(context.Background(), HTTPRequest{})
	if err != nil {
		t.Fatalf("HTTPDo second: %v", err)
	}
	if resp2.Headers["x-one"][0] != "a" || string(resp2.Body) != "ok" {
		t.Fatalf("response was not deep copied from caller result: %+v body=%s", resp2.Headers, resp2.Body)
	}
}

func TestClientCallbackErrorAndInvalidEnvelopeAreRedacted(t *testing.T) {
	caller := &recordingCaller{resp: []byte(`{"ok":false,"error":{"code":"token_invalidated","message":"Bearer tok path email@example.com","status":401,"retryable":true}}`)}
	client := NewClient(caller, "")
	_, err := client.ListAuth(context.Background())
	var callbackErr *CallbackError
	if !errors.As(err, &callbackErr) {
		t.Fatalf("expected CallbackError, got %T %v", err, err)
	}
	if callbackErr.Code != "token_invalidated" || callbackErr.Status != 401 || !callbackErr.Retryable {
		t.Fatalf("unexpected callback error: %+v", callbackErr)
	}
	if got := err.Error(); bytes.Contains([]byte(got), []byte("Bearer")) || bytes.Contains([]byte(got), []byte("email@example.com")) || bytes.Contains([]byte(got), []byte("path")) {
		t.Fatalf("error leaked sensitive text: %q", got)
	}

	caller.resp = []byte(`{"ok":true}`)
	_, err = client.ListAuth(context.Background())
	if !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("expected invalid envelope, got %v", err)
	}
}

func TestClientRejectsInvalidResultAndHonorsContextCancellation(t *testing.T) {
	caller := &recordingCaller{resp: envelope(t, `{"files":"bad"}`)}
	client := NewClient(caller, "")
	if _, err := client.ListAuth(context.Background()); !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("expected invalid result, got %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	caller.resp = envelope(t, `{"files":[]}`)
	if _, err := client.ListAuth(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}
