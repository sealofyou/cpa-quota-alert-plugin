package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/config"
)

func TestRenderUsesPublicSafeDefaults(t *testing.T) {
	msg, err := Render(RenderInput{
		Kind: KindLow,
		Summary: map[string]any{
			"total":            0.41,
			"partial":          true,
			"unresolved_count": 43,
		},
		OccurredAt:        time.Date(2026, 9, 11, 4, 0, 0, 0, time.UTC),
		LowThreshold:      1.5,
		RecoveryThreshold: 1.6,
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Subject != "[CPA quota] remaining 0.41 Plus-week equivalents" {
		t.Fatalf("subject=%q", msg.Subject)
	}
	for _, leaked := range []string{"eryu", "qq.com", "vps", "5.253", "sealofyou", "@"} {
		if strings.Contains(strings.ToLower(msg.Subject+"\n"+msg.Body), leaked) {
			t.Fatalf("default mail leaked %q", leaked)
		}
	}
	if !strings.Contains(msg.Body, "Low threshold: 1.50") || !strings.Contains(msg.Body, "Unresolved accounts: 43") {
		t.Fatalf("body missing aggregate fields:\n%s", msg.Body)
	}
}

func TestRenderAppliesOperatorOverride(t *testing.T) {
	msg, err := Render(RenderInput{
		Kind: KindRecovery,
		Summary: map[string]any{
			"total": json.Number("2.00"),
		},
		OccurredAt:        time.Date(2026, 9, 11, 5, 0, 0, 0, time.UTC),
		LowThreshold:      1.5,
		RecoveryThreshold: 1.6,
		Templates: config.MailConfig{Templates: map[string]config.MailTemplate{
			KindRecovery: {
				Subject: "quota recovered {{total}}",
				Body:    "total={{total}} recovery={{recovery_threshold}}",
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Subject != "quota recovered 2.00" {
		t.Fatalf("subject=%q", msg.Subject)
	}
	if msg.Body != "total=2.00 recovery=1.60" {
		t.Fatalf("body=%q", msg.Body)
	}
}

func TestRenderRejectsUnknownKindAndPlaceholder(t *testing.T) {
	if _, err := Render(RenderInput{Kind: "custom"}); err == nil {
		t.Fatal("expected unknown kind to fail")
	}
	if err := ValidateMailTemplates(config.MailConfig{Templates: map[string]config.MailTemplate{
		KindLow: {Subject: "hi {{secret}}", Body: "body"},
	}}); err == nil {
		t.Fatal("expected unknown placeholder to fail")
	}
	if err := ValidateMailTemplates(config.MailConfig{Templates: map[string]config.MailTemplate{
		KindLow: {Subject: "hi\nBcc: x", Body: "body"},
	}}); err == nil {
		t.Fatal("expected subject newline to fail")
	}
}

func TestFormatUnknownPlansIsStableAndAccountFree(t *testing.T) {
	got := formatUnknownPlans(map[string]any{"enterprise": 2.0, "pro": 1.0})
	if got != "enterprise=2, pro=1" {
		t.Fatalf("plans=%q", got)
	}
}
