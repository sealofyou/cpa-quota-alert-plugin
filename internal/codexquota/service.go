package codexquota

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/abi"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/config"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/quota"
)

const userAgent = "cpa-quota-alert-plugin/0.1"

type Host interface {
	ListAuth(ctx context.Context) ([]abi.HostAuthFileEntry, error)
	GetAuth(ctx context.Context, authIndex string) (abi.AuthFile, error)
	HTTPDo(ctx context.Context, req abi.HTTPRequest) (abi.HTTPResponse, error)
}

type ABIHost struct {
	Client *abi.Client
}

func (h ABIHost) ListAuth(ctx context.Context) ([]abi.HostAuthFileEntry, error) {
	return h.Client.ListAuth(ctx)
}

func (h ABIHost) GetAuth(ctx context.Context, authIndex string) (abi.AuthFile, error) {
	return h.Client.GetAuth(ctx, authIndex)
}

func (h ABIHost) HTTPDo(ctx context.Context, req abi.HTTPRequest) (abi.HTTPResponse, error) {
	return h.Client.HTTPDo(ctx, req)
}

type Service struct {
	host    Host
	cfg     config.Config
	Sleeper func(context.Context, time.Duration) error
}

type Checker = Service

type DiscoveryStats struct {
	Selected     int
	Skipped      int
	Disabled     int
	Unavailable  int
	RuntimeOnly  int
	MissingIndex int
	GetFailed    int
	Unreadable   int
}

type Credential struct {
	AccessToken string
	AccountID   string
}

func NewService(host Host, cfg config.Config) *Service {
	return &Service{host: host, cfg: cfg, Sleeper: sleepContext}
}

func (s *Service) Check(ctx context.Context) ([]quota.AccountObservation, DiscoveryStats) {
	if s == nil || s.host == nil {
		return nil, DiscoveryStats{}
	}
	listCtx, cancel := s.withTimeout(ctx)
	entries, err := s.host.ListAuth(listCtx)
	cancel()
	if err != nil {
		return nil, DiscoveryStats{Unreadable: 1}
	}
	selected, stats := selectCodex(entries)
	if len(selected) == 0 || ctx.Err() != nil {
		return nil, stats
	}
	limit := s.cfg.Concurrency
	if limit <= 0 {
		limit = 1
	}
	jobs := make(chan job)
	results := make([]workerResult, len(selected))
	var wg sync.WaitGroup
	workers := limit
	if workers > len(selected) {
		workers = len(selected)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				obs, localStats := s.checkOne(ctx, job.entry)
				results[job.index] = workerResult{observation: obs, stats: localStats}
			}
		}()
	}
	for i, entry := range selected {
		if ctx.Err() != nil {
			break
		}
		jobs <- job{index: i, entry: entry}
	}
	close(jobs)
	wg.Wait()
	out := make([]quota.AccountObservation, 0, len(results))
	for _, result := range results {
		stats.add(result.stats)
		if result.observation != (quota.AccountObservation{}) {
			out = append(out, result.observation)
		}
	}
	return out, stats
}

type workerResult struct {
	observation quota.AccountObservation
	stats       DiscoveryStats
}

func (s *DiscoveryStats) add(other DiscoveryStats) {
	s.Selected += other.Selected
	s.Skipped += other.Skipped
	s.Disabled += other.Disabled
	s.Unavailable += other.Unavailable
	s.RuntimeOnly += other.RuntimeOnly
	s.MissingIndex += other.MissingIndex
	s.GetFailed += other.GetFailed
	s.Unreadable += other.Unreadable
}

type job struct {
	index int
	entry abi.HostAuthFileEntry
}

func selectCodex(entries []abi.HostAuthFileEntry) ([]abi.HostAuthFileEntry, DiscoveryStats) {
	selected := make([]abi.HostAuthFileEntry, 0, len(entries))
	stats := DiscoveryStats{}
	for _, entry := range entries {
		if entry.Disabled {
			stats.Disabled++
			continue
		}
		if !isCodex(entry.Provider) && !isCodex(entry.Type) {
			stats.Skipped++
			continue
		}
		if entry.Unavailable {
			stats.Unavailable++
		}
		stats.Selected++
		selected = append(selected, entry)
	}
	return selected, stats
}

func (s *Service) checkOne(ctx context.Context, entry abi.HostAuthFileEntry) (quota.AccountObservation, DiscoveryStats) {
	if entry.AuthIndex == "" {
		return s.observeError("missing_auth_index"), DiscoveryStats{MissingIndex: 1}
	}
	if entry.RuntimeOnly {
		return s.observeError("runtime_auth_unreadable"), DiscoveryStats{RuntimeOnly: 1}
	}
	getCtx, cancel := s.withTimeout(ctx)
	auth, err := s.host.GetAuth(getCtx, entry.AuthIndex)
	cancel()
	if err != nil {
		return s.observeError("auth_get_error"), DiscoveryStats{GetFailed: 1}
	}
	cred, err := ParseCredential(auth.JSON)
	if err != nil {
		return s.observeError("credential_invalid"), DiscoveryStats{Unreadable: 1}
	}
	body, code := s.fetchQuota(ctx, cred)
	if code != "" {
		return s.observeError(code), DiscoveryStats{}
	}
	obs, _ := quota.ObserveAccount(quota.AccountInput{Enabled: true, Body: body}, s.rules())
	return obs, DiscoveryStats{}
}

func (s *Service) fetchQuota(ctx context.Context, cred Credential) ([]byte, string) {
	attempts := s.cfg.RetryAttempts + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastCode string
	for attempt := 0; attempt < attempts; attempt++ {
		reqCtx, cancel := s.withTimeout(ctx)
		resp, err := s.host.HTTPDo(reqCtx, abi.HTTPRequest{
			Method: "GET",
			URL:    s.cfg.QuotaURL,
			Headers: map[string][]string{
				"Authorization":      []string{"Bearer " + cred.AccessToken},
				"Chatgpt-Account-Id": []string{cred.AccountID},
				"Accept":             []string{"application/json"},
				"User-Agent":         []string{userAgent},
			},
		})
		cancel()
		if err != nil {
			lastCode = "host_http_error"
			var cb *abi.CallbackError
			if errors.As(err, &cb) && cb.Retryable && attempt+1 < attempts {
				s.sleep(ctx, attempt)
				continue
			}
			return nil, lastCode
		}
		if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
			return append([]byte(nil), resp.Body...), ""
		}
		lastCode = s.responseErrorCode(resp.StatusCode, resp.Body)
		if resp.StatusCode >= 500 && resp.StatusCode <= 599 && attempt+1 < attempts {
			s.sleep(ctx, attempt)
			continue
		}
		return nil, lastCode
	}
	return nil, lastCode
}

func (s *Service) observeError(code string) quota.AccountObservation {
	obs, _ := quota.ObserveAccount(quota.AccountInput{Enabled: true, ErrorCode: code}, s.rules())
	return obs
}

func (s *Service) rules() quota.Rules {
	rules := quota.Rules{PlanRules: map[string]quota.PlanRule{}}
	for name, rule := range s.cfg.PlanRules {
		rules.PlanRules[name] = quota.PlanRule{Name: rule.Name, Aliases: append([]string(nil), rule.Aliases...), Window: rule.Window, Weight: rule.Weight}
	}
	for ignored := range s.cfg.IgnoredPlans {
		rules.IgnoredPlans = append(rules.IgnoredPlans, ignored)
	}
	for code := range s.cfg.TerminalErrorCodes {
		rules.TerminalErrorCodes = append(rules.TerminalErrorCodes, code)
	}
	return rules
}

func (s *Service) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.cfg.RequestTimeoutSeconds <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, time.Duration(s.cfg.RequestTimeoutSeconds)*time.Second)
}

func (s *Service) sleep(ctx context.Context, attempt int) {
	if s.Sleeper == nil {
		return
	}
	_ = s.Sleeper(ctx, time.Duration(attempt+1)*time.Millisecond)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func ParseCredential(data []byte) (Credential, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return Credential{}, stableError("credential_invalid")
	}
	if raw == nil {
		return Credential{}, stableError("credential_invalid")
	}
	accessToken, _ := raw["access_token"].(string)
	if strings.TrimSpace(accessToken) == "" {
		return Credential{}, stableError("credential_missing_access_token")
	}
	accountID := firstString(raw, "account_id", "chatgpt_account_id", "https://api.openai.com/auth.chatgpt_account_id")
	if accountID == "" {
		idToken, _ := raw["id_token"].(string)
		if idToken != "" {
			claims, err := parseJWTPayload(idToken)
			if err != nil {
				return Credential{}, stableError("credential_invalid_jwt")
			}
			accountID = firstString(claims, "chatgpt_account_id", "https://api.openai.com/auth.chatgpt_account_id")
		}
	}
	if strings.TrimSpace(accountID) == "" {
		return Credential{}, stableError("credential_missing_account_id")
	}
	return Credential{AccessToken: accessToken, AccountID: accountID}, nil
}

func parseJWTPayload(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, errors.New("invalid jwt")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil || claims == nil {
		return nil, errors.New("invalid jwt payload")
	}
	return claims, nil
}

func (s *Service) responseErrorCode(status int, body []byte) string {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err == nil && raw != nil {
		if code := firstResponseCode(raw); s.isTerminalErrorCode(code) {
			return code
		}
	}
	return "http_" + strconv.Itoa(status)
}

func firstResponseCode(raw map[string]any) string {
	if code := nestedCode(raw, "error"); code != "" {
		return code
	}
	if code := nestedCode(raw, "detail"); code != "" {
		return code
	}
	if code, _ := raw["code"].(string); strings.TrimSpace(code) != "" {
		return strings.TrimSpace(code)
	}
	return ""
}

func (s *Service) isTerminalErrorCode(code string) bool {
	if strings.TrimSpace(code) == "" {
		return false
	}
	normalized := normalizeByObservation(code)
	for terminal := range s.cfg.TerminalErrorCodes {
		if normalizeByObservation(terminal) == normalized {
			return true
		}
	}
	return false
}
func nestedCode(raw map[string]any, key string) string {
	obj, _ := raw[key].(map[string]any)
	if obj == nil {
		return ""
	}
	code, _ := obj["code"].(string)
	return strings.TrimSpace(code)
}

func isCodex(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "codex")
}

func firstString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := raw[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type stableError string

func (e stableError) Error() string { return string(e) }

func normalizeByObservation(code string) string {
	obs, _ := quota.ObserveAccount(quota.AccountInput{Enabled: true, ErrorCode: code}, quota.Rules{})
	return obs.UnresolvedCode
}
