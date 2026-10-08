package quota

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// tierRules mirrors a pool whose upstream tiers each carry their own operator-confirmed
// weight. The numbers are operator configuration, not an OpenAI entitlement statement.
func tierRules() Rules {
	return Rules{
		PlanRules: map[string]PlanRule{
			"plus":    {Name: "plus", Aliases: []string{"plus", "chatgptplus"}, Window: Window7d, Weight: 1},
			"k12":     {Name: "k12", Aliases: []string{"k12"}, Window: Window5h, Weight: 0.2},
			"prolite": {Name: "prolite", Aliases: []string{"prolite"}, Window: Window7d, Weight: 5},
			"pro":     {Name: "pro", Aliases: []string{"pro"}, Window: Window7d, Weight: 20},
		},
		IgnoredPlans:       []string{"free"},
		TerminalErrorCodes: []string{"token_invalidated"},
	}
}

func account(planType string, usedPercent float64, windowSeconds int) AccountInput {
	body := fmt.Sprintf(
		`{"plan_type":%q,"rate_limit":{"secondary_window":{"used_percent":%g,"limit_window_seconds":%d}}}`,
		planType, usedPercent, windowSeconds,
	)
	return AccountInput{Enabled: true, Body: []byte(body)}
}

// A mixed pool must weigh every account by the tier that account itself reports. Before the
// per-tier mapping, a Pro 100 account either stopped the total or, if folded into the Pro
// rule, inflated it fourfold.
func TestMixedTierPoolUsesEachAccountsOwnWeight(t *testing.T) {
	snap, err := Aggregate([]AccountInput{
		account("plus", 60, Window7dSeconds),    // 40% * 1  = 0.40
		account("prolite", 20, Window7dSeconds), // 80% * 5  = 4.00
		account("pro", 50, Window7dSeconds),     // 50% * 20 = 10.00
	}, tierRules(), time.Now())
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if snap.Partial || len(snap.UnknownPlanCounts) != 0 {
		t.Fatalf("snapshot must be complete: %+v", snap)
	}
	if snap.Total != 14.4 {
		t.Fatalf("total = %v, want 14.4 (0.4 + 4 + 10)", snap.Total)
	}
	if snap.Plans["prolite"].Remaining != 4 || snap.Plans["pro"].Remaining != 10 {
		t.Fatalf("per-tier breakdown wrong: %+v", snap.Plans)
	}
	if snap.Plans["prolite"].Accounts != 1 || snap.Plans["pro"].Accounts != 1 || snap.Plans["plus"].Accounts != 1 {
		t.Fatalf("per-tier account counts wrong: %+v", snap.Plans)
	}
}

// Swapping the two Pro tiers between accounts must change the total. A rule set that gave
// both tiers one weight would return the same number here.
func TestProTiersAreNotInterchangeable(t *testing.T) {
	rules := tierRules()
	liteHeavy, err := Aggregate([]AccountInput{account("prolite", 50, Window7dSeconds)}, rules, time.Now())
	if err != nil {
		t.Fatalf("Aggregate prolite: %v", err)
	}
	proHeavy, err := Aggregate([]AccountInput{account("pro", 50, Window7dSeconds)}, rules, time.Now())
	if err != nil {
		t.Fatalf("Aggregate pro: %v", err)
	}
	if liteHeavy.Total != 2.5 || proHeavy.Total != 10 {
		t.Fatalf("prolite=%v pro=%v, want 2.5 and 10", liteHeavy.Total, proHeavy.Total)
	}
}

// An upstream tier with no configured rule keeps the safe stop: no weighted total, and a
// plan-change signal naming the tier.
func TestUnconfiguredProTierStillStopsTheTotal(t *testing.T) {
	_, err := Aggregate([]AccountInput{
		account("plus", 60, Window7dSeconds),
		account("promax", 20, Window7dSeconds),
	}, tierRules(), time.Now())
	var changed *PlanChangedError
	if !errors.As(err, &changed) || changed.UnknownPlans["promax"] != 1 {
		t.Fatalf("promax must raise a plan change, got %v", err)
	}
}

// A failed account must not hide a mixed-tier pool's unknown tier, and must not let the
// remaining subtotal pass as a complete total.
func TestPartialQueryKeepsUnknownTierAndMarksPartial(t *testing.T) {
	snap, err := Aggregate([]AccountInput{
		account("plus", 60, Window7dSeconds),
		account("prolite", 20, Window7dSeconds),
		account("promax", 20, Window7dSeconds),
		{Enabled: true, ErrorCode: "hosthttperror"},
	}, tierRules(), time.Now())
	var changed *PlanChangedError
	if !errors.As(err, &changed) || changed.UnknownPlans["promax"] != 1 {
		t.Fatalf("unknown tier must survive a partial query, got %v", err)
	}
	if !snap.Partial || snap.UnresolvedCodeCounts["hosthttperror"] != 1 {
		t.Fatalf("partial query must be recorded: %+v", snap)
	}
}

// Terminal credentials still count as zero capacity and must not be mistaken for an
// unknown tier in a mixed pool.
func TestTerminalCredentialInMixedTierPoolStaysComputable(t *testing.T) {
	snap, err := Aggregate([]AccountInput{
		account("prolite", 20, Window7dSeconds),
		{Enabled: true, ErrorCode: "token_invalidated"},
	}, tierRules(), time.Now())
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if snap.Partial || snap.Total != 4 || snap.TerminalErrorCounts["tokeninvalidated"] != 1 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
}
