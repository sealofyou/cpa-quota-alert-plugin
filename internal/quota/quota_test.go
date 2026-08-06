package quota

import (
	"errors"
	"testing"
	"time"
)

func testRules() Rules {
	return Rules{
		PlanRules: map[string]PlanRule{
			"plus": {Name: "plus", Aliases: []string{"plus", "chat gpt-plus"}, Window: Window7d, Weight: 1},
			"team": {Name: "team", Aliases: []string{"team"}, Window: Window7d, Weight: 1},
			"k12":  {Name: "k12", Aliases: []string{"k-12"}, Window: Window7d, Weight: 0.2},
			"pro":  {Name: "pro", Aliases: []string{"pro20"}, Window: Window7d, Weight: 20},
		},
		IgnoredPlans:       []string{"free"},
		TerminalErrorCodes: []string{"401", "403"},
	}
}

func TestParseWhamWindowsAndFallback(t *testing.T) {
	q, err := ParseWham([]byte(`{
		"planType":"Plus",
		"rateLimit":{
			"primary":{"usedPercent":25},
			"secondary":{"used_percent":40,"limit_window_seconds":604800}
		}
	}`))
	if err != nil {
		t.Fatalf("ParseWham: %v", err)
	}
	if q.Windows[Window5h].RemainingPercent != 75 || q.Windows[Window7d].RemainingPercent != 60 {
		t.Fatalf("unexpected windows: %+v", q.Windows)
	}
}

func TestAggregatePlansTerminalAndUnresolved(t *testing.T) {
	now := time.Date(2026, 8, 6, 1, 2, 3, 0, time.UTC)
	snap, err := Aggregate([]AccountInput{
		{Enabled: true, Body: []byte(`{"plan_type":"ChatGPT Plus","rate_limit":{"secondary":{"used_percent":25,"limit_window_seconds":604800}}}`)},
		{Enabled: true, Body: []byte(`{"plan_type":"free","rate_limit":{"secondary":{"used_percent":1,"limit_window_seconds":604800}}}`)},
		{Enabled: true, ErrorCode: "401"},
		{Enabled: true, ErrorCode: "timeout"},
	}, testRules(), now)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if snap.TotalByWindow[Window7d] != 0.75 || !snap.Partial {
		t.Fatalf("unexpected aggregate: %+v", snap)
	}
	if snap.TerminalErrorCounts["401"] != 1 || snap.UnresolvedCodeCounts["timeout"] != 1 {
		t.Fatalf("unexpected error counts: %+v %+v", snap.TerminalErrorCounts, snap.UnresolvedCodeCounts)
	}
}

func TestUnknownPlanBlocksTotalWithPlanChanged(t *testing.T) {
	_, err := Aggregate([]AccountInput{{Enabled: true, Body: []byte(`{"plan_type":"enterprise","rate_limit":{"secondary":{"used_percent":25,"limit_window_seconds":604800}}}`)}}, testRules(), time.Now())
	var changed *PlanChangedError
	if !errors.As(err, &changed) || changed.UnknownPlans["enterprise"] != 1 {
		t.Fatalf("expected PlanChangedError, got %v", err)
	}
}

func TestAllUnresolvedNotComputable(t *testing.T) {
	_, err := Aggregate([]AccountInput{{Enabled: true, ErrorCode: "timeout"}}, testRules(), time.Now())
	if !errors.Is(err, ErrNotComputable) {
		t.Fatalf("expected ErrNotComputable, got %v", err)
	}
}

func TestMissingPercentRequiresResetBackedExhaustion(t *testing.T) {
	q, err := ParseWham([]byte(`{"plan_type":"plus","rate_limit":{"secondary":{"limit_window_seconds":604800,"limit_reached":true,"reset_at":"soon"}}}`))
	if err != nil {
		t.Fatalf("ParseWham exhausted: %v", err)
	}
	if q.Windows[Window7d].RemainingPercent != 0 {
		t.Fatalf("expected exhausted remaining 0, got %+v", q.Windows[Window7d])
	}
	if _, err := ParseWham([]byte(`{"plan_type":"plus","rate_limit":{"secondary":{"limit_window_seconds":604800}}}`)); err == nil {
		t.Fatalf("expected missing percent error")
	}
}
