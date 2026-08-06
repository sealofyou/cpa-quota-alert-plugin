package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseYAMLValidNestedConfiguration(t *testing.T) {
	data := []byte(`dry_run: true
concurrency: 3
plan_rules:
  - name: k12
    aliases: [k12]
    window: 5h
    weight: 0.2
ignored_plans: [free]
terminal_error_codes: [token_revoked]
`)
	cfg, err := ParseYAML(data, func(string) string { return "" })
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if !cfg.DryRun || cfg.Concurrency != 3 || cfg.PlanRules["k12"].Weight != 0.2 || !cfg.TerminalErrorCodes.Contains("token_revoked") {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestParseYAMLOperatorConfirmedPro20Example(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate yaml_test.go")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "examples", "operator-confirmed-pro20.yaml"))
	if err != nil {
		t.Fatalf("read example: %v", err)
	}
	expectedEnv := map[string]string{
		"CPA_QUOTA_ALERT_SMTP_USER":     "example-user",
		"CPA_QUOTA_ALERT_SMTP_PASSWORD": "example-password",
		"CPA_QUOTA_ALERT_SMTP_TO":       "ops@example.com",
		"CPA_QUOTA_ALERT_SMTP_FROM":     "alerts@example.com",
	}
	seenEnv := map[string]bool{}
	var unexpectedEnv []string
	cfg, err := ParseYAML(data, func(name string) string {
		value, ok := expectedEnv[name]
		if !ok {
			unexpectedEnv = append(unexpectedEnv, name)
			return ""
		}
		seenEnv[name] = true
		return value
	})
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if len(unexpectedEnv) != 0 {
		t.Fatalf("unexpected environment queries: %v", unexpectedEnv)
	}
	for name := range expectedEnv {
		if !seenEnv[name] {
			t.Fatalf("expected environment query %q was not used", name)
		}
	}
	k12 := cfg.PlanRules["k12"]
	if k12.Window != Window5h || k12.Weight != 0.2 {
		t.Fatalf("k12 rule = %+v, want window %q weight 0.2", k12, Window5h)
	}
	if got := cfg.Aliases["pro"]; got != "pro20" {
		t.Fatalf("alias pro = %q, want pro20", got)
	}
	pro20 := cfg.PlanRules["pro20"]
	if pro20.Window != Window7d || pro20.Weight != 20 {
		t.Fatalf("pro20 rule = %+v, want window %q weight 20", pro20, Window7d)
	}
}

func TestParseYAMLEmptyUsesDefaults(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("  \n")} {
		cfg, err := ParseYAML(data, func(string) string { return "" })
		if err != nil {
			t.Fatalf("ParseYAML(%q): %v", data, err)
		}
		if cfg.Concurrency != 4 || cfg.LowThreshold != 1.5 || len(cfg.PlanRules) != 2 {
			t.Fatalf("defaults not applied: %+v", cfg)
		}
	}
}

func TestDecodeYAMLRejectsUnsafeStructures(t *testing.T) {
	tests := []struct {
		name string
		data string
		want error
	}{
		{"malformed", "value: [", ErrYAMLSyntax},
		{"multiple documents", "a: 1\n---\nb: 2\n", ErrYAMLMultipleDoc},
		{"duplicate key", "a: 1\na: 2\n", ErrYAMLDuplicate},
		{"non-string key", "1: value\n", ErrYAMLMapKey},
		{"nested non-string key", "outer:\n  true: value\n", ErrYAMLMapKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeYAML([]byte(tt.data))
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
}

func TestDecodeYAMLLimitsSizeAndDoesNotEchoSource(t *testing.T) {
	secret := "example-private-value"
	field := "smtp_" + "password"
	_, err := DecodeYAML([]byte(field + ": [" + secret))
	if !errors.Is(err, ErrYAMLSyntax) || strings.Contains(err.Error(), secret) {
		t.Fatalf("syntax error leaked source: %v", err)
	}
	_, err = DecodeYAML(make([]byte, MaxYAMLBytes+1))
	if !errors.Is(err, ErrYAMLTooLarge) {
		t.Fatalf("size error = %v", err)
	}
}

func TestParseYAMLStillRejectsPlaintextSecrets(t *testing.T) {
	secret := "example-" + "private-value"
	field := "smtp_" + "password"
	_, err := ParseYAML([]byte(field+": "+secret+"\n"), func(string) string { return "" })
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("plaintext secret was accepted or leaked: %v", err)
	}
}
