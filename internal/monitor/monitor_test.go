package monitor

import (
	"errors"
	"testing"
	"time"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/quota"
)

func snapshot(total float64) *quota.Snapshot {
	return &quota.Snapshot{Total: total, TotalByWindow: map[string]float64{quota.Window7d: total}}
}

func TestEvaluateUsesAggregatedTotalForMixedWindows(t *testing.T) {
	now := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	st := Evaluate(State{}, Input{
		Snapshot: &quota.Snapshot{
			Total: 1.6,
			TotalByWindow: map[string]float64{
				quota.Window7d: 1.4,
				quota.Window5h: 0.2,
			},
		},
		Channels: []string{"smtp"},
	}, now)
	if st.LowActive || len(st.PendingEvents) != 0 {
		t.Fatalf("mixed windows should use aggregated total and stay above low threshold: %+v", st)
	}
	if st.LastValidTotal != 1.6 {
		t.Fatalf("last valid total should be aggregated total, got %v", st.LastValidTotal)
	}
}

func TestLowReminderRecoveryHysteresis(t *testing.T) {
	now := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	st := Evaluate(State{}, Input{Snapshot: snapshot(1.49), Channels: []string{"smtp"}}, now)
	if !st.LowActive || len(st.PendingEvents) != 1 || st.PendingEvents[0].Kind != EventLow {
		t.Fatalf("expected first low event: %+v", st)
	}
	st = MarkDelivered(st, st.PendingEvents[0].ID, "smtp", true, now.Add(time.Minute))
	st = Evaluate(st, Input{Snapshot: snapshot(1.55), Channels: []string{"smtp"}}, now.Add(time.Hour))
	if len(st.PendingEvents) != 0 || !st.LowActive {
		t.Fatalf("hysteresis band should stay quiet and low-active: %+v", st)
	}
	st = Evaluate(st, Input{Snapshot: snapshot(1.4), Channels: []string{"smtp"}}, now.Add(25*time.Hour))
	if len(st.PendingEvents) != 1 || st.PendingEvents[0].Kind != EventLowReminder {
		t.Fatalf("expected 24h reminder: %+v", st.PendingEvents)
	}
	st = MarkDelivered(st, st.PendingEvents[0].ID, "smtp", true, now.Add(25*time.Hour+time.Minute))
	st = Evaluate(st, Input{Snapshot: snapshot(1.6), Channels: []string{"smtp"}}, now.Add(26*time.Hour))
	if st.LowActive || len(st.PendingEvents) != 1 || st.PendingEvents[0].Kind != EventRecovery {
		t.Fatalf("expected recovery: %+v", st)
	}
}

func TestThreeFailuresOnlyNotifyOnceUntilSuccess(t *testing.T) {
	now := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	st := State{}
	for i := 0; i < 5; i++ {
		st = Evaluate(st, Input{Err: quota.ErrNotComputable, Channels: []string{"smtp"}}, now.Add(time.Duration(i)*time.Minute))
	}
	if st.ConsecutiveFailures != 5 || len(st.PendingEvents) != 1 || st.PendingEvents[0].Kind != EventDataError {
		t.Fatalf("expected one data_error after three failures: %+v", st)
	}
	st = Evaluate(st, Input{Snapshot: snapshot(2)}, now.Add(time.Hour))
	if st.ConsecutiveFailures != 0 || st.ErrorActive || st.LastValidTotal != 2 {
		t.Fatalf("success should reset error state: %+v", st)
	}
	for i := 0; i < 3; i++ {
		st = Evaluate(st, Input{Err: errors.New("bad json"), Channels: []string{"smtp"}}, now.Add(2*time.Hour+time.Duration(i)*time.Minute))
	}
	if got := countKind(st.PendingEvents, EventDataError); got != 2 {
		t.Fatalf("expected retrigger after success reset, got %d pending events: %+v", got, st.PendingEvents)
	}
}

func TestPlanChangedDoesNotIncrementFailuresAndResolvesOnSuccess(t *testing.T) {
	now := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	err := &quota.PlanChangedError{UnknownPlans: map[string]int{"enterprise": 1}}
	st := Evaluate(State{}, Input{Err: err, Channels: []string{}}, now)
	st = Evaluate(st, Input{Err: err, Channels: []string{}}, now.Add(time.Minute))
	if st.ConsecutiveFailures != 0 || !st.PlanChangedActive || countKind(st.PendingEvents, EventPlanChanged) != 1 {
		t.Fatalf("plan changed should notify once without failures: %+v", st)
	}
	if len(st.PendingEvents[0].Delivery) != 0 {
		t.Fatalf("empty channel event must remain observable but not delivered: %+v", st.PendingEvents[0])
	}
	st = Evaluate(st, Input{Snapshot: snapshot(2)}, now.Add(time.Hour))
	if st.PlanChangedActive || st.LastValidTotal != 2 {
		t.Fatalf("known plan success should clear plan_changed: %+v", st)
	}
}

func TestPerChannelDeliveryRetriesOnlyFailures(t *testing.T) {
	now := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	st := Evaluate(State{}, Input{Snapshot: snapshot(1.0), Channels: []string{"smtp", "webhook"}}, now)
	id := st.PendingEvents[0].ID
	st = MarkDelivered(st, id, "smtp", true, now.Add(time.Minute))
	if st.LastLowDeliveredAt.IsZero() || len(st.PendingEvents) != 1 {
		t.Fatalf("smtp success should update low delivery but keep pending for webhook: %+v", st)
	}
	if st.PendingEvents[0].Delivery["smtp"] != "delivered" || st.PendingEvents[0].Delivery["webhook"] != "pending" {
		t.Fatalf("unexpected delivery map: %+v", st.PendingEvents[0].Delivery)
	}
	st = MarkDelivered(st, id, "webhook", false, now.Add(2*time.Minute))
	if len(st.PendingEvents) != 1 || st.PendingEvents[0].Delivery["webhook"] != "failed" {
		t.Fatalf("failed webhook should stay pending: %+v", st.PendingEvents)
	}
	st = MarkDelivered(st, id, "webhook", true, now.Add(3*time.Minute))
	if len(st.PendingEvents) != 0 || len(st.DeliveredEvents) != 1 {
		t.Fatalf("all channels delivered should complete event: %+v", st)
	}
}

func countKind(events []Event, kind string) int {
	count := 0
	for _, event := range events {
		if event.Kind == kind {
			count++
		}
	}
	return count
}

func TestEventIDsUniqueForSameKindAndTimestamp(t *testing.T) {
	now := time.Date(2026, 8, 6, 0, 0, 0, 123, time.UTC)
	st := State{}
	appendEvent(&st, EventLowReminder, now, []string{"smtp"}, map[string]any{"total": 1.0})
	appendEvent(&st, EventLowReminder, now, []string{"smtp"}, map[string]any{"total": 1.0})
	if len(st.PendingEvents) != 2 {
		t.Fatalf("expected two pending events: %+v", st)
	}
	if st.PendingEvents[0].ID == st.PendingEvents[1].ID {
		t.Fatalf("same kind and timestamp events must have unique IDs: %s", st.PendingEvents[0].ID)
	}
	if st.EventSequence != 2 {
		t.Fatalf("event sequence should be persisted and incremented: %+v", st)
	}
}
func TestPlanChangedSummaryDeepCopied(t *testing.T) {
	now := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	unknown := map[string]int{"enterprise": 1}
	st := Evaluate(State{}, Input{Err: &quota.PlanChangedError{UnknownPlans: unknown}}, now)
	unknown["enterprise"] = 99
	unknown["other"] = 42
	got := st.PendingEvents[0].Summary["unknown_plans"].(map[string]int)
	if got["enterprise"] != 1 || got["other"] != 0 {
		t.Fatalf("pending summary should not alias source map: %+v", got)
	}
	dry := DryRun(st, Input{Err: &quota.PlanChangedError{UnknownPlans: unknown}}, now.Add(time.Minute))
	got["enterprise"] = 7
	dryGot := dry.PendingEvents[0].Summary["unknown_plans"].(map[string]int)
	if dryGot["enterprise"] != 1 {
		t.Fatalf("dry-run state should deep-copy pending summaries: %+v", dryGot)
	}
}
