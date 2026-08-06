package monitor

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/quota"
)

const (
	EventLow         = "low"
	EventLowReminder = "low_reminder"
	EventRecovery    = "recovery"
	EventDataError   = "data_error"
	EventPlanChanged = "plan_changed"
)

type State struct {
	LowActive               bool             `json:"low_active,omitempty"`
	PlanChangedActive       bool             `json:"plan_changed_active,omitempty"`
	ErrorActive             bool             `json:"error_active,omitempty"`
	ConsecutiveFailures     int              `json:"consecutive_failures,omitempty"`
	LastLowDeliveredAt      time.Time        `json:"last_low_delivered_at,omitempty"`
	LastRecoveryDeliveredAt time.Time        `json:"last_recovery_delivered_at,omitempty"`
	LastErrorDeliveredAt    time.Time        `json:"last_error_delivered_at,omitempty"`
	LastPlanDeliveredAt     time.Time        `json:"last_plan_delivered_at,omitempty"`
	LastValidTotal          float64          `json:"last_valid_total,omitempty"`
	LastValidAt             time.Time        `json:"last_valid_at,omitempty"`
	LastErrorCode           string           `json:"last_error_code,omitempty"`
	PendingEvents           []Event          `json:"pending_events,omitempty"`
	DeliveredEvents         map[string]Event `json:"delivered_events,omitempty"`
	EventSequence           int64            `json:"event_sequence,omitempty"`
}

type Event struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind"`
	CreatedAt time.Time         `json:"created_at"`
	Summary   map[string]any    `json:"summary,omitempty"`
	Delivery  map[string]string `json:"delivery,omitempty"`
}

type Input struct {
	Snapshot          *quota.Snapshot
	Err               error
	Channels          []string
	LowThreshold      float64
	RecoveryThreshold float64
	ReminderSeconds   int
	FailureAlertCount int
}

func Evaluate(current State, input Input, now time.Time) State {
	next := cloneState(current)
	if next.DeliveredEvents == nil {
		next.DeliveredEvents = map[string]Event{}
	}
	if input.LowThreshold == 0 {
		input.LowThreshold = 1.5
	}
	if input.RecoveryThreshold == 0 {
		input.RecoveryThreshold = 1.6
	}
	if input.ReminderSeconds == 0 {
		input.ReminderSeconds = 86400
	}
	if input.FailureAlertCount == 0 {
		input.FailureAlertCount = 3
	}
	now = now.UTC()

	var planChanged *quota.PlanChangedError
	if errors.As(input.Err, &planChanged) {
		next.ConsecutiveFailures = 0
		next.ErrorActive = false
		next.LastErrorCode = ""
		if !next.PlanChangedActive {
			next.PlanChangedActive = true
			appendEvent(&next, EventPlanChanged, now, input.Channels, map[string]any{
				"unknown_plans": planChanged.UnknownPlans,
			})
		}
		return next
	}

	if input.Err != nil {
		next.ConsecutiveFailures++
		next.LastErrorCode = stableErrorCode(input.Err)
		if next.ConsecutiveFailures >= input.FailureAlertCount && !next.ErrorActive {
			next.ErrorActive = true
			appendEvent(&next, EventDataError, now, input.Channels, map[string]any{
				"consecutive_failures": next.ConsecutiveFailures,
				"error_code":           next.LastErrorCode,
			})
		}
		return next
	}
	if input.Snapshot == nil {
		return next
	}

	total := input.Snapshot.Total
	next.ConsecutiveFailures = 0
	next.ErrorActive = false
	next.LastErrorCode = ""
	next.LastValidTotal = total
	next.LastValidAt = now
	if next.PlanChangedActive {
		next.PlanChangedActive = false
	}

	switch {
	case total < input.LowThreshold:
		if !next.LowActive {
			next.LowActive = true
			appendEvent(&next, EventLow, now, input.Channels, map[string]any{"total": total})
		} else if !next.LastLowDeliveredAt.IsZero() && now.Sub(next.LastLowDeliveredAt) >= time.Duration(input.ReminderSeconds)*time.Second {
			if !hasPendingKind(next.PendingEvents, EventLowReminder) {
				appendEvent(&next, EventLowReminder, now, input.Channels, map[string]any{"total": total})
			}
		}
	case total >= input.RecoveryThreshold:
		if next.LowActive {
			next.LowActive = false
			appendEvent(&next, EventRecovery, now, input.Channels, map[string]any{"total": total})
		}
	default:
	}
	return next
}

func MarkDelivered(current State, eventID, channel string, delivered bool, now time.Time) State {
	next := cloneState(current)
	now = now.UTC()
	for i := range next.PendingEvents {
		event := &next.PendingEvents[i]
		if event.ID != eventID {
			continue
		}
		if event.Delivery == nil {
			event.Delivery = map[string]string{}
		}
		if delivered {
			event.Delivery[channel] = "delivered"
			updateDeliveredTime(&next, event.Kind, now)
		} else if event.Delivery[channel] != "delivered" {
			event.Delivery[channel] = "failed"
		}
		if len(event.Delivery) > 0 && allConfiguredDelivered(*event) {
			if next.DeliveredEvents == nil {
				next.DeliveredEvents = map[string]Event{}
			}
			next.DeliveredEvents[event.ID] = *event
			next.PendingEvents = slices.Delete(next.PendingEvents, i, i+1)
		}
		return next
	}
	return next
}

func DryRun(current State, input Input, now time.Time) State {
	return Evaluate(cloneState(current), input, now)
}

func appendEvent(state *State, kind string, now time.Time, channels []string, summary map[string]any) {
	state.EventSequence++
	state.PendingEvents = append(state.PendingEvents, newEvent(kind, now, state.EventSequence, channels, summary))
}

func newEvent(kind string, now time.Time, sequence int64, channels []string, summary map[string]any) Event {
	delivery := map[string]string{}
	for _, channel := range channels {
		channel = strings.TrimSpace(channel)
		if channel != "" {
			delivery[channel] = "pending"
		}
	}
	id := stableEventID(kind, now, sequence, summary)
	return Event{ID: id, Kind: kind, CreatedAt: now.UTC(), Summary: cloneSummary(summary), Delivery: delivery}
}

func stableEventID(kind string, now time.Time, sequence int64, summary map[string]any) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%v", kind, now.UTC().Format(time.RFC3339Nano), sequence, summary)))
	return kind + "-" + hex.EncodeToString(hash[:8])
}

func stableErrorCode(err error) string {
	if errors.Is(err, quota.ErrNotComputable) {
		return "not_computable"
	}
	text := strings.ToLower(err.Error())
	text = strings.NewReplacer(" ", "_", "-", "_", ":", "").Replace(text)
	if len(text) > 48 {
		text = text[:48]
	}
	if text == "" {
		return "error"
	}
	return text
}

func updateDeliveredTime(state *State, kind string, now time.Time) {
	switch kind {
	case EventLow, EventLowReminder:
		state.LastLowDeliveredAt = now
	case EventRecovery:
		state.LastRecoveryDeliveredAt = now
	case EventDataError:
		state.LastErrorDeliveredAt = now
	case EventPlanChanged:
		state.LastPlanDeliveredAt = now
	}
}

func allConfiguredDelivered(event Event) bool {
	if len(event.Delivery) == 0 {
		return false
	}
	for _, status := range event.Delivery {
		if status != "delivered" {
			return false
		}
	}
	return true
}

func hasPendingKind(events []Event, kind string) bool {
	return slices.ContainsFunc(events, func(event Event) bool { return event.Kind == kind })
}

func cloneState(state State) State {
	out := state
	out.PendingEvents = append([]Event(nil), state.PendingEvents...)
	for i := range out.PendingEvents {
		out.PendingEvents[i].Summary = cloneSummary(out.PendingEvents[i].Summary)
		out.PendingEvents[i].Delivery = cloneStringMap(out.PendingEvents[i].Delivery)
	}
	out.DeliveredEvents = map[string]Event{}
	for key, event := range state.DeliveredEvents {
		event.Summary = cloneSummary(event.Summary)
		event.Delivery = cloneStringMap(event.Delivery)
		out.DeliveredEvents[key] = event
	}
	return out
}

func cloneSummary(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := map[string]any{}
	for key, value := range in {
		out[key] = cloneValue(value)
	}
	return out
}

func cloneValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return cloneSummary(v)
	case map[string]int:
		out := map[string]int{}
		for key, item := range v {
			out[key] = item
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = cloneValue(item)
		}
		return out
	case []string:
		return append([]string(nil), v...)
	case []int:
		return append([]int(nil), v...)
	default:
		return v
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := map[string]string{}
	for key, value := range in {
		out[key] = value
	}
	return out
}
