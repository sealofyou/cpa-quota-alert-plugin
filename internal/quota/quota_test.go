package quota

import (
	"encoding/json"
	"errors"
	"math"
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

func TestDurationFieldsClassifyWindows(t *testing.T) {
	q, err := ParseWham([]byte(`{
		"plan_type":"plus",
		"rate_limit":{
			"primary":{"used_percent":10,"duration":18000},
			"secondary":{"used_percent":20,"durationSeconds":604800}
		}
	}`))
	if err != nil {
		t.Fatalf("ParseWham duration fields: %v", err)
	}
	if q.Windows[Window5h].RemainingPercent != 90 || q.Windows[Window7d].RemainingPercent != 80 {
		t.Fatalf("unexpected duration classification: %+v", q.Windows)
	}
	q, err = ParseWham([]byte(`{"plan_type":"plus","rate_limit":{"used_percent":30,"duration_seconds":604800}}`))
	if err != nil {
		t.Fatalf("ParseWham duration_seconds: %v", err)
	}
	if q.Windows[Window7d].RemainingPercent != 70 {
		t.Fatalf("unexpected duration_seconds classification: %+v", q.Windows)
	}
}

func TestMixedSuccessAndUnknownPlanReturnsPlanChanged(t *testing.T) {
	snap, err := Aggregate([]AccountInput{
		{Enabled: true, Body: []byte(`{"plan_type":"plus","rate_limit":{"secondary":{"used_percent":50,"limit_window_seconds":604800}}}`)},
		{Enabled: true, Body: []byte(`{"plan_type":"enterprise","rate_limit":{"secondary":{"used_percent":25,"limit_window_seconds":604800}}}`)},
	}, testRules(), time.Now())
	var changed *PlanChangedError
	if !errors.As(err, &changed) || changed.UnknownPlans["enterprise"] != 1 {
		t.Fatalf("expected PlanChangedError for mixed success+unknown, got snap=%+v err=%v", snap, err)
	}
}

func TestEmptyInputNotComputable(t *testing.T) {
	_, err := Aggregate(nil, testRules(), time.Now())
	if !errors.Is(err, ErrNotComputable) {
		t.Fatalf("expected ErrNotComputable for empty input, got %v", err)
	}
}

func TestInvalidUsedPercentIsLocalParseError(t *testing.T) {
	snap, err := Aggregate([]AccountInput{
		{Enabled: true, Body: []byte(`{"plan_type":"plus","rate_limit":{"secondary":{"used_percent":-1,"limit_window_seconds":604800}}}`)},
		{Enabled: true, Body: []byte(`{"plan_type":"team","rate_limit":{"secondary":{"used_percent":101,"limit_window_seconds":604800}}}`)},
	}, testRules(), time.Now())
	if !errors.Is(err, ErrNotComputable) || snap.UnresolvedCodeCounts["parse_error"] != 2 {
		t.Fatalf("invalid used percent should be local parse errors and all-unresolved not computable: snap=%+v err=%v", snap, err)
	}
}

func TestExhaustionInferenceRequiresResetInfo(t *testing.T) {
	for _, body := range []string{
		`{"plan_type":"plus","rate_limit":{"secondary":{"limit_window_seconds":604800,"allowed":false}}}`,
		`{"plan_type":"plus","rate_limit":{"secondary":{"limit_window_seconds":604800,"limit_reached":true}}}`,
	} {
		if _, err := ParseWham([]byte(body)); err == nil {
			t.Fatalf("expected missing percent error without reset info for %s", body)
		}
	}
	for _, body := range []string{
		`{"plan_type":"plus","rate_limit":{"secondary":{"limit_window_seconds":604800,"allowed":false,"reset_seconds":10}}}`,
		`{"plan_type":"plus","rate_limit":{"secondary":{"limit_window_seconds":604800,"limit_reached":true,"resetAt":"soon"}}}`,
	} {
		q, err := ParseWham([]byte(body))
		if err != nil {
			t.Fatalf("expected reset-backed exhaustion for %s: %v", body, err)
		}
		if q.Windows[Window7d].RemainingPercent != 0 {
			t.Fatalf("expected zero remaining: %+v", q.Windows[Window7d])
		}
	}
}

func TestDurationRejectsNonFiniteFractionalAndOverflow(t *testing.T) {
	bad := []any{18000.5, json.Number("18000.5"), math.NaN(), math.Inf(1), float64(^uint(0)) * 4}
	for _, duration := range bad {
		_, err := parseWindow(map[string]any{"used_percent": 1, "duration": duration}, "")
		if err == nil {
			t.Fatalf("expected duration error for %#v", duration)
		}
	}
}

func TestAggregateRoundsAfterAccumulatingRawValues(t *testing.T) {
	inputs := []AccountInput{
		{Enabled: true, Body: []byte(`{"plan_type":"plus","rate_limit":{"secondary":{"used_percent":66.6667,"limit_window_seconds":604800}}}`)},
		{Enabled: true, Body: []byte(`{"plan_type":"plus","rate_limit":{"secondary":{"used_percent":66.6667,"limit_window_seconds":604800}}}`)},
		{Enabled: true, Body: []byte(`{"plan_type":"plus","rate_limit":{"secondary":{"used_percent":66.6667,"limit_window_seconds":604800}}}`)},
	}
	snap, err := Aggregate(inputs, testRules(), time.Now())
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if snap.TotalByWindow[Window7d] != 1 || snap.Plans["plus"].Remaining != 1 || snap.Total != 1 {
		t.Fatalf("expected final-rounding total 1.0000, got %+v", snap)
	}
}
