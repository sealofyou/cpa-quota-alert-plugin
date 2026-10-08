package config

import (
	"strings"
	"testing"
)

func planRuleConfig(rules ...map[string]any) map[string]any {
	candidates := make([]any, 0, len(rules))
	for _, rule := range rules {
		candidates = append(candidates, rule)
	}
	return map[string]any{"plan_rules": candidates}
}

// A single rule must never absorb two upstream subscription tiers. Folding "prolite"
// into the Pro rule would silently bill a Pro 100 account at the Pro 200 weight, which is
// exactly the mis-scaling this validation exists to stop.
func TestParseRejectsOneRuleClaimingTwoUpstreamTiers(t *testing.T) {
	for _, aliases := range [][]any{
		{"pro", "prolite"},
		{"pro", "promax"},
		{"plus", "team"},
	} {
		raw := planRuleConfig(map[string]any{
			"name": "bundled", "aliases": aliases, "window": Window7d, "weight": float64(20),
		})
		_, err := Parse(raw, func(string) string { return "" })
		if err == nil {
			t.Fatalf("aliases %v must be rejected", aliases)
		}
		if !strings.Contains(err.Error(), "distinct upstream plan types") {
			t.Fatalf("aliases %v: unexpected error %v", aliases, err)
		}
	}
}

// The canonical name counts as a claim too, so renaming the rule cannot smuggle a second
// tier past the check.
func TestParseRejectsCanonicalNamePlusForeignUpstreamAlias(t *testing.T) {
	raw := planRuleConfig(map[string]any{
		"name": "pro", "aliases": []any{"pro_lite"}, "window": Window7d, "weight": float64(20),
	})
	if _, err := Parse(raw, func(string) string { return "" }); err == nil ||
		!strings.Contains(err.Error(), "distinct upstream plan types") {
		t.Fatalf("pro rule with a prolite alias must be rejected, got %v", err)
	}
}

// Operator-local spellings are not upstream values, so they stay usable as aliases.
func TestParseAllowsOperatorLocalAliasesAlongsideOneUpstreamTier(t *testing.T) {
	raw := planRuleConfig(
		map[string]any{"name": "plus", "aliases": []any{"plus", "chatgptplus"}, "window": Window7d, "weight": float64(1)},
		map[string]any{"name": "prolite", "aliases": []any{"pro_lite", "chatgptpro100"}, "window": Window7d, "weight": float64(5)},
		map[string]any{"name": "pro", "aliases": []any{"pro", "chatgptpro20"}, "window": Window7d, "weight": float64(20)},
	)
	cfg, err := Parse(raw, func(string) string { return "" })
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Aliases["prolite"] != "prolite" || cfg.Aliases["chatgptpro100"] != "prolite" {
		t.Fatalf("prolite aliases not mapped: %+v", cfg.Aliases)
	}
	if cfg.PlanRules["prolite"].Weight != 5 || cfg.PlanRules["pro"].Weight != 20 {
		t.Fatalf("per-tier weights not preserved: %+v", cfg.PlanRules)
	}
	if _, mapped := cfg.Aliases["promax"]; mapped {
		t.Fatal("promax must stay unmapped")
	}
}

func TestIsKnownUpstreamPlan(t *testing.T) {
	for _, value := range []string{"pro", "ProLite", "pro_lite", "promax", "plus", "k12", "edu_pro"} {
		if !IsKnownUpstreamPlan(value) {
			t.Fatalf("%q should be a known upstream plan", value)
		}
	}
	for _, value := range []string{"pro20", "pro5x", "chatgptplus", "chatgptpro20", ""} {
		if IsKnownUpstreamPlan(value) {
			t.Fatalf("%q is an operator-local spelling, not an upstream plan", value)
		}
	}
}
