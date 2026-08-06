package quota

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

const (
	Window5h = "5h"
	Window7d = "7d"

	Window5hSeconds = 18000
	Window7dSeconds = 604800
)

var ErrNotComputable = errors.New("quota snapshot is not computable")

type PlanChangedError struct {
	UnknownPlans map[string]int
}

func (e *PlanChangedError) Error() string {
	return fmt.Sprintf("unrecognized plan types: %v", e.UnknownPlans)
}

type Window struct {
	Name             string
	UsedPercent      float64
	RemainingPercent float64
	ResetKnown       bool
}

type AccountQuota struct {
	PlanType string
	Windows  map[string]Window
}

type PlanRule struct {
	Name    string
	Aliases []string
	Window  string
	Weight  float64
}

type Rules struct {
	PlanRules          map[string]PlanRule
	IgnoredPlans       []string
	TerminalErrorCodes []string
}

type AccountInput struct {
	Enabled   bool
	PlanType  string
	Body      []byte
	ErrorCode string
}

type AccountObservation struct {
	Plan              string
	CanonicalPlan     string
	WeightedRemaining float64
	TerminalZero      bool
	TerminalCode      string
	UnresolvedCode    string
	UnknownPlan       string
	Partial           bool
	Success           bool
	Window            string
}

type PlanBreakdown struct {
	Accounts          int
	TerminalZeroCount int
	Remaining         float64
}

type Snapshot struct {
	Timestamp            time.Time
	Total                float64
	TotalByWindow        map[string]float64
	Plans                map[string]PlanBreakdown
	TerminalErrorCounts  map[string]int
	UnresolvedCodeCounts map[string]int
	UnknownPlanCounts    map[string]int
	Partial              bool
}

func ParseWham(data []byte) (AccountQuota, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return AccountQuota{}, err
	}
	plan := getString(raw, "plan_type", "planType")
	rateLimit, _ := getObject(raw, "rate_limit", "rateLimit")
	if rateLimit == nil {
		rateLimit = raw
	}
	windows := map[string]Window{}
	parseNamedWindow := func(name string, fallback string) error {
		obj, ok := getObject(rateLimit, name)
		if !ok {
			return nil
		}
		window, err := parseWindow(obj, fallback)
		if err != nil {
			return fmt.Errorf("%s window: %w", name, err)
		}
		windows[window.Name] = window
		return nil
	}
	for _, item := range []struct {
		name     string
		fallback string
	}{
		{"primary", Window5h},
		{"secondary", Window7d},
		{"primary_window", Window5h},
		{"secondary_window", Window7d},
		{"primaryWindow", Window5h},
		{"secondaryWindow", Window7d},
	} {
		if err := parseNamedWindow(item.name, item.fallback); err != nil {
			return AccountQuota{}, err
		}
	}
	if len(windows) == 0 {
		window, err := parseWindow(rateLimit, "")
		if err != nil {
			return AccountQuota{}, err
		}
		windows[window.Name] = window
	}
	return AccountQuota{PlanType: plan, Windows: windows}, nil
}

func ObserveAccount(input AccountInput, rules Rules) (AccountObservation, error) {
	if !input.Enabled {
		return AccountObservation{}, nil
	}
	if input.ErrorCode != "" {
		if containsNormalized(rules.TerminalErrorCodes, input.ErrorCode) {
			return AccountObservation{TerminalZero: true, TerminalCode: normalize(input.ErrorCode)}, nil
		}
		return AccountObservation{UnresolvedCode: normalize(input.ErrorCode), Partial: true}, nil
	}
	quota, err := ParseWham(input.Body)
	if err != nil {
		return AccountObservation{UnresolvedCode: "parse_error", Partial: true}, err
	}
	if input.PlanType != "" {
		quota.PlanType = input.PlanType
	}
	return observeQuota(quota, rules)
}

func Aggregate(inputs []AccountInput, rules Rules, now time.Time) (Snapshot, error) {
	observations := make([]AccountObservation, 0, len(inputs))
	for _, input := range inputs {
		obs, err := ObserveAccount(input, rules)
		if err != nil && obs.UnresolvedCode == "" {
			return Snapshot{}, err
		}
		if obs != (AccountObservation{}) {
			observations = append(observations, obs)
		}
	}
	return AggregateObservations(observations, now)
}

func AggregateObservations(observations []AccountObservation, now time.Time) (Snapshot, error) {
	snap := Snapshot{
		Timestamp:            now.UTC(),
		TotalByWindow:        map[string]float64{},
		Plans:                map[string]PlanBreakdown{},
		TerminalErrorCounts:  map[string]int{},
		UnresolvedCodeCounts: map[string]int{},
		UnknownPlanCounts:    map[string]int{},
	}
	successes := 0
	terminalZeros := 0
	for _, obs := range observations {
		if obs.UnknownPlan != "" {
			snap.UnknownPlanCounts[obs.UnknownPlan]++
			continue
		}
		if obs.UnresolvedCode != "" {
			snap.UnresolvedCodeCounts[obs.UnresolvedCode]++
			snap.Partial = true
			continue
		}
		if obs.TerminalZero {
			code := obs.TerminalCode
			if code == "" {
				code = "terminal"
			}
			snap.TerminalErrorCounts[code]++
			terminalZeros++
			continue
		}
		if obs.Success {
			successes++
			total := round4(obs.WeightedRemaining)
			snap.Total += total
			snap.TotalByWindow[obs.Window] = round4(snap.TotalByWindow[obs.Window] + total)
			breakdown := snap.Plans[obs.CanonicalPlan]
			breakdown.Accounts++
			breakdown.Remaining = round4(breakdown.Remaining + total)
			snap.Plans[obs.CanonicalPlan] = breakdown
		}
	}
	snap.Total = round4(snap.Total)
	if len(snap.UnknownPlanCounts) > 0 {
		return snap, &PlanChangedError{UnknownPlans: snap.UnknownPlanCounts}
	}
	if successes == 0 && terminalZeros == 0 {
		return snap, ErrNotComputable
	}
	return snap, nil
}

func observeQuota(account AccountQuota, rules Rules) (AccountObservation, error) {
	plan := normalize(account.PlanType)
	if plan == "" {
		return AccountObservation{UnresolvedCode: "missing_plan", Partial: true}, nil
	}
	if containsNormalized(rules.IgnoredPlans, plan) {
		return AccountObservation{}, nil
	}
	rule, ok := findRule(plan, rules.PlanRules)
	if !ok {
		return AccountObservation{Plan: plan, UnknownPlan: plan}, nil
	}
	window, ok := account.Windows[rule.Window]
	if !ok {
		return AccountObservation{UnresolvedCode: "missing_" + rule.Window, Partial: true}, nil
	}
	if window.RemainingPercent < 0 || window.RemainingPercent > 100 {
		return AccountObservation{UnresolvedCode: "invalid_remaining", Partial: true}, nil
	}
	return AccountObservation{
		Plan:              plan,
		CanonicalPlan:     rule.Name,
		WeightedRemaining: window.RemainingPercent / 100 * rule.Weight,
		Success:           true,
		Window:            rule.Window,
	}, nil
}

func parseWindow(raw map[string]any, fallback string) (Window, error) {
	windowName, err := windowName(raw, fallback)
	if err != nil {
		return Window{}, err
	}
	used, ok := getFloat(raw, "used_percent", "usedPercent")
	if !ok {
		if hasResetInfo(raw) && exhausted(raw) {
			used = 100
		} else {
			return Window{}, errors.New("used_percent missing without reset-backed exhausted signal")
		}
	}
	if used < 0 || used > 100 {
		return Window{}, fmt.Errorf("used_percent %.4f outside 0..100", used)
	}
	return Window{
		Name:             windowName,
		UsedPercent:      used,
		RemainingPercent: round4(100 - used),
		ResetKnown:       hasResetInfo(raw),
	}, nil
}

func windowName(raw map[string]any, fallback string) (string, error) {
	seconds, ok := getFloat(raw, "limit_window_seconds", "limitWindowSeconds", "window_seconds", "windowSeconds")
	if ok {
		switch int(seconds) {
		case Window5hSeconds:
			return Window5h, nil
		case Window7dSeconds:
			return Window7d, nil
		default:
			return "", fmt.Errorf("unsupported limit_window_seconds %.0f", seconds)
		}
	}
	if fallback == Window5h || fallback == Window7d {
		return fallback, nil
	}
	return "", errors.New("limit_window_seconds is required outside primary/secondary fallback")
}

func hasResetInfo(raw map[string]any) bool {
	for _, key := range []string{"reset_at", "resetAt", "resets_at", "resetsAt", "reset_seconds", "resetSeconds"} {
		if _, ok := raw[key]; ok {
			return true
		}
	}
	return false
}

func exhausted(raw map[string]any) bool {
	if reached, ok := getBool(raw, "limit_reached", "limitReached"); ok && reached {
		return true
	}
	if allowed, ok := getBool(raw, "allowed"); ok && !allowed {
		return true
	}
	return false
}

func findRule(alias string, rules map[string]PlanRule) (PlanRule, bool) {
	alias = normalize(alias)
	for _, rule := range rules {
		if normalize(rule.Name) == alias {
			return normalizeRule(rule), true
		}
		for _, candidate := range rule.Aliases {
			if normalize(candidate) == alias {
				return normalizeRule(rule), true
			}
		}
	}
	return PlanRule{}, false
}

func normalizeRule(rule PlanRule) PlanRule {
	rule.Name = normalize(rule.Name)
	return rule
}

func containsNormalized(values []string, needle string) bool {
	needle = normalize(needle)
	return slices.ContainsFunc(values, func(value string) bool { return normalize(value) == needle })
}

func normalize(value string) string {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.NewReplacer(" ", "", "_", "", "-", "").Replace(lower)
}

func round4(value float64) float64 {
	return math.Round(value*10000) / 10000
}

func getObject(raw map[string]any, keys ...string) (map[string]any, bool) {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			if obj, ok := value.(map[string]any); ok {
				return obj, true
			}
		}
	}
	return nil, false
}

func getString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			if text, ok := value.(string); ok {
				return text
			}
		}
	}
	return ""
}

func getFloat(raw map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			switch v := value.(type) {
			case float64:
				return v, true
			case int:
				return float64(v), true
			case json.Number:
				f, err := v.Float64()
				return f, err == nil
			}
		}
	}
	return 0, false
}

func getBool(raw map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			if b, ok := value.(bool); ok {
				return b, true
			}
		}
	}
	return false, false
}
