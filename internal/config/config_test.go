package config

import "testing"

func TestDefaultsAndValidation(t *testing.T) {
	cfg, err := Parse(map[string]any{}, nil)
	if err != nil {
		t.Fatalf("Parse defaults: %v", err)
	}
	if cfg.Concurrency != 4 || cfg.RequestTimeoutSeconds != 45 || cfg.RetryAttempts != 2 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.PlanRules["plus"].Weight != 1 || cfg.PlanRules["plus"].Window != Window7d {
		t.Fatalf("missing conservative plus default: %+v", cfg.PlanRules)
	}
	if _, ok := cfg.PlanRules["pro"]; ok {
		t.Fatalf("pro must not be a default plan rule")
	}
	if !cfg.IgnoredPlans.Contains("free") {
		t.Fatalf("free must be ignored by default")
	}
}

func TestExplicitRulesAliasesConflictsAndSecrets(t *testing.T) {
	_, err := Parse(map[string]any{
		"plan_rules": []any{
			map[string]any{"name": "plus", "aliases": []any{"Plus", "p_l-u s"}, "window": Window7d, "weight": 1},
			map[string]any{"name": "team", "aliases": []any{"plus"}, "window": Window7d, "weight": 1},
		},
	}, nil)
	if err == nil {
		t.Fatalf("expected alias conflict")
	}
	_, err = Parse(map[string]any{"ignored_plans": []any{"free"}, "plan_rules": []any{
		map[string]any{"name": "plus", "aliases": []any{"free"}, "window": Window7d, "weight": 1},
	}}, nil)
	if err == nil {
		t.Fatalf("expected ignored alias conflict")
	}
	_, err = Parse(map[string]any{"smtp": map[string]any{"smtp_password": "plain"}}, nil)
	if err == nil {
		t.Fatalf("expected plaintext secret rejection")
	}
}

func TestExplicitK12AndProAllowedOnlyWhenConfigured(t *testing.T) {
	cfg, err := Parse(map[string]any{"plan_rules": []any{
		map[string]any{"name": "plus", "aliases": []any{"plus"}, "window": Window7d, "weight": 1},
		map[string]any{"name": "k12", "aliases": []any{"k-12"}, "window": Window7d, "weight": 0.2},
		map[string]any{"name": "pro", "aliases": []any{"pro20"}, "window": Window7d, "weight": 20},
	}}, nil)
	if err != nil {
		t.Fatalf("Parse explicit rules: %v", err)
	}
	if cfg.PlanRules["k12"].Weight != 0.2 || cfg.PlanRules["pro"].Weight != 20 {
		t.Fatalf("explicit weights not preserved: %+v", cfg.PlanRules)
	}
}

func TestThresholdAndURLValidation(t *testing.T) {
	if _, err := Parse(map[string]any{"low_threshold": 1.6, "recovery_threshold": 1.5}, nil); err == nil {
		t.Fatalf("expected threshold validation error")
	}
	if _, err := Parse(map[string]any{"quota_url": "/relative"}, nil); err == nil {
		t.Fatalf("expected absolute URL validation error")
	}
	if _, err := Parse(map[string]any{"quota_url": "https://example.com/usage"}, nil); err == nil {
		t.Fatalf("expected host allowlist validation error")
	}
}
