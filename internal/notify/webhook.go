package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/config"
)

type WebhookSender struct {
	cfg    config.WebhookConfig
	getenv config.Getenv
	client *http.Client
}

type webhookEnvelope struct {
	EventID    string    `json:"event_id,omitempty"`
	EventType  string    `json:"event_type"`
	Subject    string    `json:"subject"`
	Body       string    `json:"body"`
	OccurredAt time.Time `json:"occurred_at"`
}

func NewWebhookSender(cfg config.WebhookConfig, getenv config.Getenv) *WebhookSender {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	return &WebhookSender{cfg: cfg, getenv: getenv}
}

func (s *WebhookSender) Send(ctx context.Context, msg Message) error {
	if s == nil || !s.cfg.Enabled {
		return nil
	}
	if err := validateMessage(msg); err != nil {
		return err
	}
	method := s.cfg.Method
	if method == "" {
		method = "POST"
	}
	method = strings.ToUpper(method)
	if method != "POST" && method != "PUT" {
		return stableError("webhook_runtime_config")
	}

	rawURL := strings.TrimSpace(s.getenv(s.cfg.URLEnv))
	authHeader := strings.TrimSpace(s.getenv(s.cfg.AuthHeaderEnv))
	if rawURL == "" || authHeader == "" {
		return stableError("webhook_runtime_config")
	}
	if hasHeaderInjection(rawURL) || hasHeaderInjection(authHeader) {
		return stableError("webhook_header_injection")
	}
	parsed, err := validateWebhookURL(rawURL)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(webhookEnvelope{
		EventID:    msg.EventID,
		EventType:  msg.EventType,
		Subject:    msg.Subject,
		Body:       msg.Body,
		OccurredAt: occurredAt(msg),
	})
	if err != nil {
		return stableError("webhook_payload_failed")
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, parsed.String(), bytes.NewReader(payload))
	if err != nil {
		return stableError("webhook_request_failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader)

	client := s.client
	if client == nil {
		client = &http.Client{Timeout: time.Duration(s.cfg.TimeoutSeconds) * time.Second}
	}
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client = &clone
	resp, err := client.Do(req)
	if err != nil {
		return stableError("webhook_request_failed")
	}
	defer func() {
		_, _ = io.CopyN(io.Discard, resp.Body, 4*1024)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return stableError("webhook_status_failed")
	}
	return nil
}

func validateWebhookURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Hostname() == "" {
		return nil, stableError("webhook_invalid_url")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return nil, stableError("webhook_invalid_url")
	}
	if parsed.Scheme == "https" {
		return parsed, nil
	}
	if parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()) {
		return parsed, nil
	}
	return nil, stableError("webhook_invalid_url")
}

func isLoopbackHost(host string) bool {
	host = strings.TrimRight(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
