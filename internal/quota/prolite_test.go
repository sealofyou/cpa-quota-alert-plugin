package quota

import (
	"errors"
	"testing"
	"time"
)

func TestProLiteRequiresIndependentOperatorRule(t *testing.T) {
	input := AccountInput{Enabled: true, Body: []byte(`{"plan_type":"prolite","rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000},"secondary_window":{"used_percent":40,"limit_window_seconds":604800}}}`)}
	rules := testRules()
	_, err := Aggregate([]AccountInput{input}, rules, time.Now())
	var changed *PlanChangedError
	if !errors.As(err, &changed) || changed.UnknownPlans["prolite"] != 1 {
		t.Fatalf("prolite must not inherit the existing pro weight: %v", err)
	}
	// This arbitrary test weight demonstrates operator configuration, not a product entitlement.
	rules.PlanRules["prolite"] = PlanRule{Name: "prolite", Aliases: []string{"prolite"}, Window: Window7d, Weight: 3}
	snap, err := Aggregate([]AccountInput{input}, rules, time.Now())
	if err != nil || snap.Partial || snap.Total != 1.8 || snap.Plans["prolite"].Accounts != 1 || len(snap.UnknownPlanCounts) != 0 {
		t.Fatalf("explicit rule must use its own weekly weight: snapshot=%+v error=%v", snap, err)
	}
	rules.PlanRules["prolite"] = PlanRule{Name: "prolite", Aliases: []string{"prolite"}, Window: Window7d, Weight: 5}
	snap, err = Aggregate([]AccountInput{input}, rules, time.Now())
	if err != nil || snap.Total != 3 {
		t.Fatalf("changed operator weight must be applied: snapshot=%+v error=%v", snap, err)
	}
}
