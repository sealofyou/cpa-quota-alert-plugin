package monitor

import (
	"testing"
	"time"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/quota"
)

func TestPlanChangedSurvivesPartialUntilComplete(t *testing.T) {
	for _, tc := range []struct {
		name  string
		total float64
	}{
		{"below low threshold", 0.58},
		{"above recovery threshold", 2.0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
			unknown := &quota.PlanChangedError{UnknownPlans: map[string]int{"prolite": 1}}
			input := Input{Err: unknown, Channels: []string{"smtp"}}
			st := Evaluate(State{LastValidTotal: 3, LastValidAt: now.Add(-time.Hour)}, input, now)
			st = MarkDelivered(st, st.PendingEvents[0].ID, "smtp", true, now)
			partial := &quota.Snapshot{Total: tc.total, Partial: true, UnresolvedCodeCounts: map[string]int{"host_callback": 1}}
			st = Evaluate(st, Input{Snapshot: partial, Channels: []string{"smtp"}}, now.Add(time.Minute))
			if !st.PlanChangedActive || st.LastValidTotal != 3 || !st.LastValidAt.Equal(now.Add(-time.Hour)) {
				t.Fatalf("incomplete check must preserve unknown-plan state and last complete total: %+v", st)
			}
			st = Evaluate(st, input, now.Add(2*time.Minute))
			if countKind(st.PendingEvents, EventPlanChanged) != 0 {
				t.Fatalf("same unknown plan must not notify again after incomplete check: %+v", st)
			}
			st = Evaluate(st, Input{Snapshot: snapshot(2)}, now.Add(3*time.Minute))
			if st.PlanChangedActive || st.LastValidTotal != 2 {
				t.Fatalf("complete successful check must resolve unknown-plan state: %+v", st)
			}
			st = Evaluate(st, input, now.Add(4*time.Minute))
			if !st.PlanChangedActive || countKind(st.PendingEvents, EventPlanChanged) != 1 {
				t.Fatalf("a new occurrence after complete recovery must still notify: %+v", st)
			}
		})
	}
}

func TestPlanChangedSurvivesRepeatedPartialFailures(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	st := State{PlanChangedActive: true}
	partial := &quota.Snapshot{Total: 0.5, Partial: true}
	for i := 0; i < 3; i++ {
		st = Evaluate(st, Input{Snapshot: partial, Channels: []string{"smtp"}}, now.Add(time.Duration(i)*time.Minute))
	}
	if !st.PlanChangedActive || !st.ErrorActive || st.ConsecutiveFailures != 3 || countKind(st.PendingEvents, EventDataError) != 1 || countKind(st.PendingEvents, EventLow) != 0 {
		t.Fatalf("partial failure alert must coexist with unresolved plan state without false low alerts: %+v", st)
	}
}
