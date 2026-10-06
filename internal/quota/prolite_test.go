package quota

import (
	"errors"
	"testing"
	"time"
)

func TestProLiteRequiresIndependentOperatorRule(t *testing.T) {
	input := AccountInput{Enabled: true, Body: []byte(`{"plan_type":"prolite","rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000},"secondary_window":{"used_percent":40,"limit_window_seconds":604800}}}`)}
	rules := testRules()
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

func TestBareProRemainsUnknownWithExactProRules(t *testing.T) {
	rules := testRules()
	delete(rules.PlanRules, "pro")
	rules.PlanRules["pro100"] = PlanRule{Name: "pro100", Aliases: []string{"pro100"}, Window: Window7d, Weight: 100}
	rules.PlanRules["pro200"] = PlanRule{Name: "pro200", Aliases: []string{"pro200"}, Window: Window7d, Weight: 200}
	rules.PlanRules["pro500"] = PlanRule{Name: "pro500", Aliases: []string{"pro500"}, Window: Window7d, Weight: 500}
	_, err := Aggregate([]AccountInput{{Enabled: true, Body: []byte(`{"plan_type":"pro","rate_limit":{"secondary":{"used_percent":50,"limit_window_seconds":604800}}}`)}}, rules, time.Now())
	var changed *PlanChangedError
	if !errors.As(err, &changed) || changed.UnknownPlans["pro"] != 1 {
		t.Fatalf("bare pro must remain unknown, got %v", err)
	}
}

func TestExactProTiersComputeSeparately(t *testing.T) {
	rules := testRules()
	delete(rules.PlanRules, "pro")
	rules.PlanRules["pro100"] = PlanRule{Name: "pro100", Aliases: []string{"pro100"}, Window: Window7d, Weight: 100}
	rules.PlanRules["pro200"] = PlanRule{Name: "pro200", Aliases: []string{"pro200"}, Window: Window7d, Weight: 200}
	rules.PlanRules["pro500"] = PlanRule{Name: "pro500", Aliases: []string{"pro500"}, Window: Window7d, Weight: 500}
	inputs := []AccountInput{
		{Enabled: true, Body: []byte(`{"plan_type":"pro100","rate_limit":{"secondary":{"used_percent":50,"limit_window_seconds":604800}}}`)},
		{Enabled: true, Body: []byte(`{"plan_type":"pro200","rate_limit":{"secondary":{"used_percent":50,"limit_window_seconds":604800}}}`)},
		{Enabled: true, Body: []byte(`{"plan_type":"pro500","rate_limit":{"secondary":{"used_percent":50,"limit_window_seconds":604800}}}`)},
	}
	snap, err := Aggregate(inputs, rules, time.Now())
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if snap.Total != 400 || snap.Plans["pro100"].Remaining != 50 || snap.Plans["pro200"].Remaining != 100 || snap.Plans["pro500"].Remaining != 250 {
		t.Fatalf("exact Pro tiers must remain separate: %+v", snap)
	}
}
