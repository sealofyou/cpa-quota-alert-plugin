package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/config"
)

func TestWebhookSenderSendsStableJSONEnvelope(t *testing.T) {
	var gotMethod, gotAuth string
	var got webhookEnvelope
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	sender := NewWebhookSender(config.WebhookConfig{
		Enabled:        true,
		Method:         "PUT",
		TimeoutSeconds: 5,
		URLEnv:         "WEBHOOK_URL",
		AuthHeaderEnv:  "WEBHOOK_AUTH",
	}, func(key string) string {
		switch key {
		case "WEBHOOK_URL":
			return server.URL + "/hook"
		case "WEBHOOK_AUTH":
			return "Bearer test-token"
		default:
			return ""
		}
	})
	when := time.Date(2026, 8, 6, 1, 2, 3, 0, time.UTC)
	if err := sender.Send(context.Background(), Message{EventID: "evt-1", EventType: "quota.low", Subject: "Quota low", Body: "Remaining quota is low.", OccurredAt: when}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotMethod != "PUT" || gotAuth != "Bearer test-token" {
		t.Fatalf("unexpected request method/auth: %s %s", gotMethod, gotAuth)
	}
	if got.EventID != "evt-1" || got.EventType != "quota.low" || got.Subject != "Quota low" || got.Body != "Remaining quota is low." || !got.OccurredAt.Equal(when) {
		t.Fatalf("unexpected envelope: %+v", got)
	}
}

func TestWebhookSenderRejectsUnsafeURLsAndRedactsErrors(t *testing.T) {
	cases := []string{
		"http://example.com/hook",
		"https://user:password@example.com/hook",
		"https://example.com/hook#fragment",
	}
	for _, rawURL := range cases {
		sender := NewWebhookSender(config.WebhookConfig{Enabled: true, Method: "POST", TimeoutSeconds: 1, URLEnv: "WEBHOOK_URL", AuthHeaderEnv: "WEBHOOK_AUTH"}, func(key string) string {
			if key == "WEBHOOK_URL" {
				return rawURL
			}
			return "Bearer test-token"
		})
		err := sender.Send(context.Background(), Message{EventType: "quota.low", Subject: "Quota low", Body: "body"})
		if !isStableError(err, "webhook_invalid_url") || strings.Contains(err.Error(), rawURL) || strings.Contains(err.Error(), "test-token") {
			t.Fatalf("expected redacted invalid url error for %q, got %v", rawURL, err)
		}
	}
}

func TestWebhookSenderDoesNotFollowRedirects(t *testing.T) {
	calledTarget := false
	mux := http.NewServeMux()
	mux.HandleFunc("/hook", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/target", http.StatusFound)
	})
	mux.HandleFunc("/target", func(w http.ResponseWriter, r *http.Request) {
		calledTarget = true
		w.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	sender := NewWebhookSender(config.WebhookConfig{Enabled: true, Method: "POST", TimeoutSeconds: 5, URLEnv: "WEBHOOK_URL", AuthHeaderEnv: "WEBHOOK_AUTH"}, func(key string) string {
		if key == "WEBHOOK_URL" {
			return server.URL + "/hook"
		}
		return "Bearer token"
	})
	err := sender.Send(context.Background(), Message{EventType: "quota.low", Subject: "Quota low", Body: "body"})
	if !isStableError(err, "webhook_status_failed") {
		t.Fatalf("expected redirect status failure, got %v", err)
	}
	if calledTarget {
		t.Fatalf("webhook sender followed redirect")
	}
}

func TestWebhookSenderOverridesCustomPermissiveRedirectClient(t *testing.T) {
	calledTarget := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calledTarget = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/second?marker=redirect-marker", http.StatusFound)
	}))
	defer source.Close()

	permissiveRedirectCalled := false
	customClient := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			permissiveRedirectCalled = true
			return nil
		},
	}
	sender := NewWebhookSender(config.WebhookConfig{Enabled: true, Method: "POST", TimeoutSeconds: 5, URLEnv: "WEBHOOK_URL", AuthHeaderEnv: "WEBHOOK_AUTH"}, func(key string) string {
		if key == "WEBHOOK_URL" {
			return source.URL + "/hook"
		}
		return "Bearer token"
	})
	sender.client = customClient

	err := sender.Send(context.Background(), Message{EventType: "quota.low", Subject: "Quota low", Body: "body"})
	if !isStableError(err, "webhook_status_failed") {
		t.Fatalf("expected redirect status failure, got %v", err)
	}
	if calledTarget {
		t.Fatalf("webhook sender followed redirect to second server")
	}
	if permissiveRedirectCalled {
		t.Fatalf("custom permissive redirect policy must be overridden")
	}
	if customClient.CheckRedirect == nil {
		t.Fatalf("caller-owned client must not be mutated")
	}
	if got := err.Error(); strings.Contains(got, target.URL) || strings.Contains(got, "redirect-marker") {
		t.Fatalf("error must not leak redirect location: %q", got)
	}
}

func TestWebhookSenderDrainsBoundedResponseBodyBeforeClose(t *testing.T) {
	body := &countingReadCloser{reader: strings.NewReader(strings.Repeat("x", 8*1024))}
	sender := NewWebhookSender(config.WebhookConfig{Enabled: true, Method: "POST", TimeoutSeconds: 5, URLEnv: "WEBHOOK_URL", AuthHeaderEnv: "WEBHOOK_AUTH"}, func(key string) string {
		if key == "WEBHOOK_URL" {
			return "http://localhost/hook"
		}
		return "Bearer token"
	})
	sender.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Body:       body,
			Header:     make(http.Header),
		}, nil
	})}

	if err := sender.Send(context.Background(), Message{EventType: "quota.low", Subject: "Quota low", Body: "body"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !body.closed {
		t.Fatalf("response body must be closed")
	}
	if body.read != 4*1024 {
		t.Fatalf("response body drain must be bounded to 4KiB, got %d", body.read)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type countingReadCloser struct {
	reader *strings.Reader
	read   int
	closed bool
}

func (b *countingReadCloser) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += n
	return n, err
}

func (b *countingReadCloser) Close() error {
	b.closed = true
	return nil
}

var _ io.ReadCloser = (*countingReadCloser)(nil)

func TestWebhookSenderRejectsHeaderInjection(t *testing.T) {
	sender := NewWebhookSender(config.WebhookConfig{Enabled: true, Method: "POST", TimeoutSeconds: 1, URLEnv: "WEBHOOK_URL", AuthHeaderEnv: "WEBHOOK_AUTH"}, func(key string) string {
		if key == "WEBHOOK_URL" {
			return "http://localhost/hook"
		}
		return "Bearer token\r\nX-Leak: yes"
	})
	err := sender.Send(context.Background(), Message{EventType: "quota.low", Subject: "Quota low", Body: "body"})
	if !isStableError(err, "webhook_header_injection") || strings.Contains(err.Error(), "X-Leak") {
		t.Fatalf("expected stable header injection error, got %v", err)
	}
}
