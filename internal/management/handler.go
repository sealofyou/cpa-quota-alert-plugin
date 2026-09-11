package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/codexquota"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/config"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/monitor"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/notify"
	"github.com/sealofyou/cpa-quota-alert-plugin/internal/quota"
)

const (
	MethodManagementRegister = "management.register"
	MethodManagementHandle   = "management.handle"

	PluginVersion = "0.1.1"

	MaxRequestBodyBytes       = 64 * 1024
	MaxManagementRequestBytes = MaxRequestBodyBytes*2 + 4096
)

type Request struct {
	HostCallbackID string              `json:"host_callback_id"`
	Method         string              `json:"Method"`
	Path           string              `json:"Path"`
	Headers        map[string][]string `json:"Headers,omitempty"`
	Query          map[string][]string `json:"Query,omitempty"`
	Body           []byte              `json:"Body,omitempty"`
}

type Response struct {
	StatusCode int                 `json:"StatusCode"`
	Headers    map[string][]string `json:"Headers,omitempty"`
	Body       []byte              `json:"Body,omitempty"`
}

type Route struct {
	Method      string
	Path        string
	Description string
}

type Registration struct {
	Routes    []Route  `json:"routes"`
	Resources []string `json:"resources"`
}

type ConfigProvider interface {
	Current(context.Context) (config.Config, bool)
}

type Checker interface {
	Check(context.Context) ([]quota.AccountObservation, codexquota.DiscoveryStats)
}

type CheckerFactory interface {
	NewChecker(hostCallbackID string, cfg config.Config) (Checker, error)
}

type Store interface {
	Load(context.Context) (monitor.State, error)
	Save(context.Context, monitor.State) error
}

type StoreFactory interface {
	NewStore(config.Config) (Store, error)
}

type Channel struct {
	Name           string
	Sender         notify.Sender
	RecipientCount int
}

type ChannelFactory interface {
	Channels(config.Config) ([]Channel, error)
}

type Clock interface {
	Now() time.Time
}

type Dependencies struct {
	ConfigProvider ConfigProvider
	CheckerFactory CheckerFactory
	StoreFactory   StoreFactory
	ChannelFactory ChannelFactory
	Clock          Clock
	Version        string
}

type Handler struct {
	deps Dependencies

	runMu   sync.Mutex
	running bool

	healthMu    sync.RWMutex
	lastCheckAt time.Time
}

func NewHandler(deps Dependencies) (*Handler, error) {
	if deps.ConfigProvider == nil || deps.CheckerFactory == nil || deps.StoreFactory == nil || deps.ChannelFactory == nil {
		return nil, errors.New("management dependencies are required")
	}
	if deps.Clock == nil {
		deps.Clock = systemClock{}
	}
	if deps.Version == "" {
		deps.Version = PluginVersion
	}
	return &Handler{deps: deps}, nil
}

func (h *Handler) Call(ctx context.Context, method string, request []byte) (response []byte, returnCode int) {
	defer func() {
		if recover() != nil {
			response = marshalResponse(errorResponse(500, "internal_error"))
			returnCode = 0
		}
	}()
	if h == nil {
		return marshalResponse(errorResponse(503, "handler_unavailable")), 0
	}
	switch method {
	case MethodManagementRegister:
		return mustJSON(Registration{Routes: protectedRoutes(), Resources: []string{}}), 0
	case MethodManagementHandle:
		return marshalResponse(h.handle(ctx, request)), 0
	case "":
		return marshalResponse(errorResponse(400, "invalid_method")), 0
	default:
		return marshalResponse(errorResponse(404, "unknown_method")), 0
	}
}

func protectedRoutes() []Route {
	return []Route{
		{Method: "POST", Path: "/cpa-quota-alert/check", Description: "Run a CPA quota check."},
		{Method: "GET", Path: "/cpa-quota-alert/status", Description: "Read CPA quota alert status."},
		{Method: "POST", Path: "/cpa-quota-alert/test-notification", Description: "Send a CPA quota alert test notification."},
	}
}

func (h *Handler) handle(ctx context.Context, raw []byte) Response {
	req, err := decodeRequest(raw)
	if errors.Is(err, errBodyTooLarge) {
		return errorResponse(413, "request_too_large")
	}
	if err != nil {
		return errorResponse(400, "invalid_request")
	}
	path := normalizePath(req.Path)
	switch path {
	case "/cpa-quota-alert/check":
		if req.Method != "POST" {
			return errorResponse(405, "method_not_allowed")
		}
		return h.check(ctx, req)
	case "/cpa-quota-alert/status":
		if req.Method != "GET" {
			return errorResponse(405, "method_not_allowed")
		}
		if len(bytes.TrimSpace(req.Body)) > 0 {
			return errorResponse(400, "unsupported_body")
		}
		return h.status(ctx)
	case "/cpa-quota-alert/test-notification":
		if req.Method != "POST" {
			return errorResponse(405, "method_not_allowed")
		}
		return h.testNotification(ctx, req)
	default:
		return errorResponse(404, "not_found")
	}
}

func decodeRequest(raw []byte) (Request, error) {
	if len(raw) > MaxManagementRequestBytes {
		return Request{}, errBodyTooLarge
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return Request{}, errors.New("empty request")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var req Request
	if err := dec.Decode(&req); err != nil {
		return Request{}, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Request{}, errors.New("extra request data")
	}
	req.Method = strings.ToUpper(strings.TrimSpace(req.Method))
	if req.Method == "" || strings.TrimSpace(req.Path) == "" {
		return Request{}, errors.New("method and path required")
	}
	if len(req.Body) > MaxRequestBodyBytes {
		return Request{}, errBodyTooLarge
	}
	return req, nil
}

func normalizePath(path string) string {
	path = strings.TrimSpace(path)
	const prefix = "/v0/management"
	if strings.HasPrefix(path, prefix+"/") {
		return strings.TrimPrefix(path, prefix)
	}
	return path
}

func (h *Handler) check(ctx context.Context, req Request) Response {
	body, err := decodeActionBody(req.Body)
	if errors.Is(err, errBodyTooLarge) {
		return errorResponse(413, "request_too_large")
	}
	if err != nil {
		return errorResponse(400, "invalid_body")
	}
	if !h.tryStart() {
		return jsonResponse(409, map[string]any{"error_code": "check_in_progress"})
	}
	defer h.finish()

	cfg, ok := h.deps.ConfigProvider.Current(ctx)
	if !ok {
		return errorResponse(503, "not_configured")
	}
	store, err := h.deps.StoreFactory.NewStore(cfg)
	if err != nil {
		return errorResponse(500, "store_unavailable")
	}
	current, err := store.Load(ctx)
	if err != nil {
		return errorResponse(500, "store_load_failed")
	}
	checker, err := h.deps.CheckerFactory.NewChecker(req.HostCallbackID, cfg)
	if err != nil || checker == nil {
		return errorResponse(500, "checker_unavailable")
	}
	channels, err := h.configuredChannels(cfg)
	if err != nil {
		return errorResponse(500, "channel_unavailable")
	}
	observations, stats := checker.Check(ctx)
	now := h.deps.Clock.Now().UTC()
	snapshot, aggregateErr := quota.AggregateObservations(observations, now)
	var snapshotPtr *quota.Snapshot
	if aggregateErr == nil {
		snapshotPtr = &snapshot
	}
	next := monitor.Evaluate(current, monitor.Input{
		Snapshot:          snapshotPtr,
		Err:               aggregateErr,
		Channels:          channelNames(channels),
		LowThreshold:      cfg.LowThreshold,
		RecoveryThreshold: cfg.RecoveryThreshold,
		ReminderSeconds:   cfg.ReminderSeconds,
		FailureAlertCount: cfg.FailureAlertCount,
	}, now)
	if err := store.Save(ctx, next); err != nil {
		return errorResponse(500, "store_save_failed")
	}
	h.recordCheck(now)
	dryRun := cfg.DryRun || body.DryRun
	result := buildCheckResponse(snapshot, aggregateErr, stats, next, dryRun)
	if dryRun {
		return jsonResponse(200, result)
	}
	deliveredState, delivery, errCode := h.deliverPending(ctx, store, next, channels, now)
	if errCode != "" {
		result["delivery"] = delivery
		result["error_code"] = errCode
		return jsonResponse(500, result)
	}
	result["delivery"] = delivery
	result["events"] = eventSummaries(deliveredState.PendingEvents)
	return jsonResponse(200, result)
}

func (h *Handler) deliverPending(ctx context.Context, store Store, current monitor.State, channels []Channel, now time.Time) (monitor.State, []deliveryResult, string) {
	state := current
	byName := map[string]notify.Sender{}
	for _, channel := range channels {
		byName[channel.Name] = channel.Sender
	}
	var results []deliveryResult
	for _, event := range append([]monitor.Event(nil), state.PendingEvents...) {
		for channel, status := range event.Delivery {
			if status == "delivered" {
				continue
			}
			sender, ok := byName[channel]
			if !ok || sender == nil {
				continue
			}
			err := sender.Send(ctx, messageForEvent(event))
			delivered := err == nil
			code := ""
			if err != nil {
				code = deliveryErrorCode(err)
			}
			state = monitor.MarkDelivered(state, event.ID, channel, delivered, now)
			results = append(results, deliveryResult{EventID: event.ID, Channel: channel, Status: deliveryStatus(delivered), ErrorCode: code})
			if err := store.Save(ctx, state); err != nil {
				return state, results, "store_save_failed"
			}
		}
	}
	return state, results, ""
}

func (h *Handler) status(ctx context.Context) Response {
	cfg, ok := h.deps.ConfigProvider.Current(ctx)
	if !ok {
		return jsonResponse(200, map[string]any{"version": h.deps.Version, "configured": false, "stale": true})
	}
	store, err := h.deps.StoreFactory.NewStore(cfg)
	if err != nil {
		return errorResponse(500, "store_unavailable")
	}
	state, err := store.Load(ctx)
	if err != nil {
		return errorResponse(500, "store_load_failed")
	}
	channels, err := h.configuredChannels(cfg)
	if err != nil {
		return errorResponse(500, "channel_unavailable")
	}
	lastCheck := h.readLastCheck()
	now := h.deps.Clock.Now().UTC()
	stale := state.LastValidAt.IsZero() || state.LastValidAt.After(now) || now.Sub(state.LastValidAt) > time.Duration(cfg.StaleAfterSeconds)*time.Second
	return jsonResponse(200, map[string]any{
		"version":             h.deps.Version,
		"configured":          true,
		"dry_run":             cfg.DryRun,
		"last_check_at":       timeOrNil(lastCheck),
		"last_valid_total":    state.LastValidTotal,
		"last_valid_at":       timeOrNil(state.LastValidAt),
		"stale":               stale,
		"low_active":          state.LowActive,
		"plan_changed_active": state.PlanChangedActive,
		"error_active":        state.ErrorActive,
		"failure_count":       state.ConsecutiveFailures,
		"pending_events":      eventSummaries(state.PendingEvents),
		"channels":            channelSummaries(channels),
	})
}

func (h *Handler) testNotification(ctx context.Context, req Request) Response {
	body, err := decodeActionBody(req.Body)
	if errors.Is(err, errBodyTooLarge) {
		return errorResponse(413, "request_too_large")
	}
	if err != nil {
		return errorResponse(400, "invalid_body")
	}
	cfg, ok := h.deps.ConfigProvider.Current(ctx)
	if !ok {
		return errorResponse(503, "not_configured")
	}
	channels, err := h.configuredChannels(cfg)
	if err != nil {
		return errorResponse(500, "channel_unavailable")
	}
	dryRun := cfg.DryRun || body.DryRun
	results := make([]deliveryResult, 0, len(channels))
	if dryRun {
		for _, channel := range channels {
			results = append(results, deliveryResult{Channel: channel.Name, Status: "would_notify"})
		}
		return jsonResponse(200, map[string]any{"would_notify": len(channels) > 0, "delivery": results})
	}
	msg := notify.Message{EventID: "management-test", EventType: "test_notification", Subject: "CPA quota alert test", Body: "This is a CPA quota alert plugin test notification.", OccurredAt: h.deps.Clock.Now().UTC()}
	for _, channel := range channels {
		code := ""
		status := "delivered"
		if err := channel.Sender.Send(ctx, msg); err != nil {
			status = "failed"
			code = deliveryErrorCode(err)
		}
		results = append(results, deliveryResult{Channel: channel.Name, Status: status, ErrorCode: code})
	}
	return jsonResponse(200, map[string]any{"would_notify": false, "delivery": results})
}

func (h *Handler) configuredChannels(cfg config.Config) ([]Channel, error) {
	channels, err := h.deps.ChannelFactory.Channels(cfg)
	if err != nil {
		return nil, err
	}
	out := make([]Channel, 0, len(channels))
	seen := map[string]struct{}{}
	for _, channel := range channels {
		name := strings.TrimSpace(strings.ToLower(channel.Name))
		if name != "smtp" && name != "webhook" {
			continue
		}
		if channel.Sender == nil {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		channel.Name = name
		out = append(out, channel)
	}
	return out, nil
}

func (h *Handler) tryStart() bool {
	h.runMu.Lock()
	defer h.runMu.Unlock()
	if h.running {
		return false
	}
	h.running = true
	return true
}

func (h *Handler) finish() {
	h.runMu.Lock()
	h.running = false
	h.runMu.Unlock()
}

func (h *Handler) recordCheck(now time.Time) {
	h.healthMu.Lock()
	h.lastCheckAt = now.UTC()
	h.healthMu.Unlock()
}

func (h *Handler) readLastCheck() time.Time {
	h.healthMu.RLock()
	defer h.healthMu.RUnlock()
	return h.lastCheckAt
}

type actionBody struct {
	DryRun bool `json:"dry_run"`
}

var errBodyTooLarge = errors.New("body too large")

func decodeActionBody(raw []byte) (actionBody, error) {
	if len(raw) > MaxRequestBodyBytes {
		return actionBody{}, errBodyTooLarge
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return actionBody{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var body actionBody
	if err := dec.Decode(&body); err != nil {
		return actionBody{}, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return actionBody{}, errors.New("extra body data")
	}
	return body, nil
}

func buildCheckResponse(snapshot quota.Snapshot, aggregateErr error, stats codexquota.DiscoveryStats, state monitor.State, dryRun bool) map[string]any {
	computable := aggregateErr == nil
	body := map[string]any{
		"computable":             computable,
		"timestamp":              snapshot.Timestamp.UTC().Format(time.RFC3339),
		"selected":               stats.Selected,
		"skipped":                stats.Skipped,
		"disabled":               stats.Disabled,
		"unavailable":            stats.Unavailable,
		"runtime_only":           stats.RuntimeOnly,
		"missing_index":          stats.MissingIndex,
		"get_failed":             stats.GetFailed,
		"unreadable":             stats.Unreadable,
		"plans":                  planBreakdown(snapshot.Plans),
		"partial":                snapshot.Partial,
		"terminal_error_counts":  snapshot.TerminalErrorCounts,
		"unresolved_code_counts": snapshot.UnresolvedCodeCounts,
		"unknown_plan_counts":    snapshot.UnknownPlanCounts,
		"events":                 eventSummaries(state.PendingEvents),
		"would_notify":           dryRun && hasDeliverablePending(state.PendingEvents),
		"delivery":               []deliveryResult{},
		"error_code":             errorCode(aggregateErr),
	}
	if computable {
		body["total"] = snapshot.Total
	}
	return body
}

func planBreakdown(plans map[string]quota.PlanBreakdown) []map[string]any {
	out := make([]map[string]any, 0, len(plans))
	for name, plan := range plans {
		out = append(out, map[string]any{
			"plan":      name,
			"accounts":  plan.Accounts,
			"terminal":  plan.TerminalZeroCount,
			"remaining": plan.Remaining,
		})
	}
	return out
}

type eventSummary struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	Delivery map[string]string `json:"delivery"`
}

func eventSummaries(events []monitor.Event) []eventSummary {
	out := make([]eventSummary, 0, len(events))
	for _, event := range events {
		out = append(out, eventSummary{ID: event.ID, Kind: event.Kind, Delivery: cloneDelivery(event.Delivery)})
	}
	return out
}

type deliveryResult struct {
	EventID   string `json:"event_id,omitempty"`
	Channel   string `json:"channel"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
}

func deliveryStatus(delivered bool) string {
	if delivered {
		return "delivered"
	}
	return "failed"
}

func deliveryErrorCode(err error) string {
	var notifyErr notify.Error
	if errors.As(err, &notifyErr) && isAllowedDeliveryCode(notifyErr.Code) {
		return notifyErr.Code
	}
	return "delivery_failed"
}

func isAllowedDeliveryCode(code string) bool {
	switch code {
	case "notify_invalid_message",
		"notify_header_injection",
		"smtp_header_injection",
		"smtp_runtime_config",
		"smtp_invalid_address",
		"smtp_connect_failed",
		"smtp_tls_failed",
		"smtp_protocol_failed",
		"smtp_auth_failed",
		"webhook_runtime_config",
		"webhook_header_injection",
		"webhook_payload_failed",
		"webhook_request_failed",
		"webhook_status_failed",
		"webhook_invalid_url":
		return true
	default:
		return false
	}
}

func messageForEvent(event monitor.Event) notify.Message {
	return notify.Message{
		EventID:    event.ID,
		EventType:  event.Kind,
		Subject:    "CPA quota alert: " + event.Kind,
		Body:       "CPA quota alert event " + event.Kind + " is pending delivery.",
		OccurredAt: event.CreatedAt,
	}
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	var planChanged *quota.PlanChangedError
	if errors.As(err, &planChanged) {
		return "plan_changed"
	}
	if errors.Is(err, quota.ErrNotComputable) {
		return "not_computable"
	}
	return "aggregate_failed"
}

func hasDeliverablePending(events []monitor.Event) bool {
	for _, event := range events {
		for _, status := range event.Delivery {
			if status != "delivered" {
				return true
			}
		}
	}
	return false
}

func channelNames(channels []Channel) []string {
	out := make([]string, 0, len(channels))
	for _, channel := range channels {
		out = append(out, channel.Name)
	}
	return out
}

func channelSummaries(channels []Channel) []map[string]any {
	out := make([]map[string]any, 0, len(channels))
	for _, channel := range channels {
		item := map[string]any{"name": channel.Name, "configured": true}
		if channel.Name == "smtp" {
			item["recipient_count"] = channel.RecipientCount
		}
		out = append(out, item)
	}
	return out
}

func timeOrNil(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339)
}

func cloneDelivery(in map[string]string) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func jsonResponse(status int, body any) Response {
	return Response{StatusCode: status, Headers: map[string][]string{"Content-Type": {"application/json"}}, Body: mustJSON(body)}
}

func errorResponse(status int, code string) Response {
	return jsonResponse(status, map[string]any{"error_code": code})
}

func marshalResponse(resp Response) []byte {
	return mustJSON(resp)
}

func mustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		return []byte(`{"error_code":"json_failed"}`)
	}
	return raw
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }
