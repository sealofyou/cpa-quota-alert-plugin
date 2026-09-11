package notify

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sealofyou/cpa-quota-alert-plugin/internal/config"
)

const (
	KindLow              = "low"
	KindLowReminder      = "low_reminder"
	KindRecovery         = "recovery"
	KindDataError        = "data_error"
	KindPlanChanged      = "plan_changed"
	KindTestNotification = "test_notification"
)

var placeholderPattern = regexp.MustCompile(`\{\{\s*([a-z_]+)\s*\}\}`)

var allowedPlaceholders = map[string]struct{}{
	"kind":                 {},
	"total":                {},
	"low_threshold":        {},
	"recovery_threshold":   {},
	"consecutive_failures": {},
	"error_code":           {},
	"unknown_plans":        {},
	"partial":              {},
	"unresolved_count":     {},
	"occurred_at":          {},
}

var defaultSubjects = map[string]string{
	KindLow:              "[CPA quota] remaining {{total}} Plus-week equivalents",
	KindLowReminder:      "[CPA quota] still low: {{total}} Plus-week equivalents",
	KindRecovery:         "[CPA quota] recovered to {{total}} Plus-week equivalents",
	KindDataError:        "[CPA quota] check failed {{consecutive_failures}} times",
	KindPlanChanged:      "[CPA quota] unrecognized plan types",
	KindTestNotification: "[CPA quota] test notification",
}

var defaultBodies = map[string]string{
	KindLow: `CPA quota is below the configured low threshold.

Remaining: {{total}}
Low threshold: {{low_threshold}}
Recovery threshold: {{recovery_threshold}}
Partial snapshot: {{partial}}
Unresolved accounts: {{unresolved_count}}
Occurred at (UTC): {{occurred_at}}

This message contains aggregate totals only. It does not include account identifiers, credentials, or host names.
`,
	KindLowReminder: `CPA quota is still below the configured low threshold.

Remaining: {{total}}
Low threshold: {{low_threshold}}
Recovery threshold: {{recovery_threshold}}
Partial snapshot: {{partial}}
Unresolved accounts: {{unresolved_count}}
Occurred at (UTC): {{occurred_at}}

This is a reminder, not a new incident. This message contains aggregate totals only.
`,
	KindRecovery: `CPA quota has reached the configured recovery threshold.

Remaining: {{total}}
Low threshold: {{low_threshold}}
Recovery threshold: {{recovery_threshold}}
Partial snapshot: {{partial}}
Unresolved accounts: {{unresolved_count}}
Occurred at (UTC): {{occurred_at}}

This message contains aggregate totals only.
`,
	KindDataError: `CPA quota checks failed repeatedly, so no new weighted total was computed.

Consecutive failures: {{consecutive_failures}}
Error code: {{error_code}}
Occurred at (UTC): {{occurred_at}}

The error code is a stable classifier. This message does not include raw upstream text, account identifiers, or credentials.
`,
	KindPlanChanged: `CPA quota found plan types that are not mapped in the operator configuration.

Unrecognized plans: {{unknown_plans}}
Occurred at (UTC): {{occurred_at}}

Weighted quota was not computed for this check. Add a local plan rule only after you confirm the intended weight.
`,
	KindTestNotification: `This is a CPA quota alert test notification.

It confirms that the configured delivery channel can send mail. It does not report a real quota change and does not include account identifiers or credentials.

Occurred at (UTC): {{occurred_at}}
`,
}

type RenderInput struct {
	Kind              string
	Summary           map[string]any
	OccurredAt        time.Time
	LowThreshold      float64
	RecoveryThreshold float64
	Templates         config.MailConfig
}

func AllowedMailKinds() []string {
	return []string{KindLow, KindLowReminder, KindRecovery, KindDataError, KindPlanChanged, KindTestNotification}
}

func IsAllowedMailKind(kind string) bool {
	_, ok := defaultSubjects[strings.TrimSpace(kind)]
	return ok
}

func ValidateMailTemplates(cfg config.MailConfig) error {
	for kind, tmpl := range cfg.Templates {
		if !IsAllowedMailKind(kind) {
			return fmt.Errorf("mail.templates.%s is not a supported event kind", kind)
		}
		if err := validateTemplateText("subject", tmpl.Subject, true); err != nil {
			return fmt.Errorf("mail.templates.%s.subject: %w", kind, err)
		}
		if err := validateTemplateText("body", tmpl.Body, false); err != nil {
			return fmt.Errorf("mail.templates.%s.body: %w", kind, err)
		}
	}
	return nil
}

func Render(input RenderInput) (Message, error) {
	kind := strings.TrimSpace(input.Kind)
	if !IsAllowedMailKind(kind) {
		return Message{}, stableError("notify_invalid_message")
	}
	subjectTmpl, bodyTmpl := resolveTemplates(kind, input.Templates)
	vars := templateVars(input)
	subject, err := applyTemplate(subjectTmpl, vars, true)
	if err != nil {
		return Message{}, stableError("notify_invalid_message")
	}
	body, err := applyTemplate(bodyTmpl, vars, false)
	if err != nil {
		return Message{}, stableError("notify_invalid_message")
	}
	return Message{
		EventType:  kind,
		Subject:    subject,
		Body:       body,
		OccurredAt: occurredAt(Message{OccurredAt: input.OccurredAt}),
	}, nil
}

func resolveTemplates(kind string, cfg config.MailConfig) (string, string) {
	subject := defaultSubjects[kind]
	body := defaultBodies[kind]
	if tmpl, ok := cfg.Templates[kind]; ok {
		if strings.TrimSpace(tmpl.Subject) != "" {
			subject = tmpl.Subject
		}
		if strings.TrimSpace(tmpl.Body) != "" {
			body = tmpl.Body
		}
	}
	return subject, body
}

func templateVars(input RenderInput) map[string]string {
	summary := input.Summary
	if summary == nil {
		summary = map[string]any{}
	}
	occurred := input.OccurredAt.UTC()
	if occurred.IsZero() {
		occurred = time.Now().UTC()
	}
	return map[string]string{
		"kind":                 strings.TrimSpace(input.Kind),
		"total":                formatNumber(firstPresent(summary, "total")),
		"low_threshold":        formatFloat(input.LowThreshold),
		"recovery_threshold":   formatFloat(input.RecoveryThreshold),
		"consecutive_failures": formatPlain(firstPresent(summary, "consecutive_failures")),
		"error_code":           formatPlain(firstPresent(summary, "error_code")),
		"unknown_plans":        formatUnknownPlans(firstPresent(summary, "unknown_plans")),
		"partial":              formatBool(firstPresent(summary, "partial")),
		"unresolved_count":     formatPlain(firstPresent(summary, "unresolved_count")),
		"occurred_at":          occurred.Format(time.RFC3339),
	}
}

func validateTemplateText(label, value string, subject bool) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must be non-empty", label)
	}
	if strings.ContainsRune(value, '\r') {
		return fmt.Errorf("%s must not contain CR", label)
	}
	if subject && strings.ContainsRune(value, '\n') {
		return fmt.Errorf("subject must be a single line")
	}
	for _, match := range placeholderPattern.FindAllStringSubmatch(value, -1) {
		if _, ok := allowedPlaceholders[match[1]]; !ok {
			return fmt.Errorf("unknown placeholder {{%s}}", match[1])
		}
	}
	return nil
}

func applyTemplate(tmpl string, vars map[string]string, subject bool) (string, error) {
	if err := validateTemplateText("template", tmpl, subject); err != nil {
		return "", err
	}
	out := placeholderPattern.ReplaceAllStringFunc(tmpl, func(match string) string {
		key := placeholderPattern.FindStringSubmatch(match)[1]
		return vars[key]
	})
	out = strings.ReplaceAll(out, "\r\n", "\n")
	if subject {
		out = strings.TrimSpace(out)
	}
	if strings.ContainsRune(out, '\r') || (subject && strings.ContainsRune(out, '\n')) {
		return "", stableError("notify_header_injection")
	}
	return out, nil
}

func firstPresent(summary map[string]any, key string) any {
	if summary == nil {
		return nil
	}
	return summary[key]
}

func formatFloat(value float64) string {
	if value == 0 {
		return "0.00"
	}
	return strconv.FormatFloat(value, 'f', 2, 64)
}

func formatNumber(value any) string {
	switch n := value.(type) {
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(n, 'f', 2, 64)
	case float32:
		return strconv.FormatFloat(float64(n), 'f', 2, 64)
	case int:
		return strconv.FormatFloat(float64(n), 'f', 2, 64)
	case int64:
		return strconv.FormatFloat(float64(n), 'f', 2, 64)
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return strings.TrimSpace(n.String())
		}
		return strconv.FormatFloat(f, 'f', 2, 64)
	default:
		return formatPlain(value)
	}
}

func formatPlain(value any) string {
	switch n := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(n)
	case bool:
		return strconv.FormatBool(n)
	case float64:
		if n == float64(int64(n)) {
			return strconv.FormatInt(int64(n), 10)
		}
		return strconv.FormatFloat(n, 'f', 2, 64)
	case int:
		return strconv.Itoa(n)
	case int64:
		return strconv.FormatInt(n, 10)
	default:
		return strings.TrimSpace(fmt.Sprint(n))
	}
}

func formatBool(value any) string {
	switch n := value.(type) {
	case bool:
		return strconv.FormatBool(n)
	case string:
		lower := strings.ToLower(strings.TrimSpace(n))
		if lower == "true" || lower == "false" {
			return lower
		}
	}
	if value == nil {
		return "false"
	}
	return formatPlain(value)
}

func formatUnknownPlans(value any) string {
	counts := map[string]int{}
	switch raw := value.(type) {
	case map[string]int:
		for plan, count := range raw {
			if plan = strings.TrimSpace(plan); plan != "" {
				counts[plan] = count
			}
		}
	case map[string]any:
		for plan, count := range raw {
			if plan = strings.TrimSpace(plan); plan != "" {
				counts[plan] = intFromAny(count)
			}
		}
	default:
		text := formatPlain(value)
		if text == "" || text == "<nil>" || text == "map[]" {
			return ""
		}
		return text
	}
	if len(counts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(counts))
	for plan := range counts {
		keys = append(keys, plan)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, plan := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", plan, counts[plan]))
	}
	return strings.Join(parts, ", ")
}

func intFromAny(value any) int {
	switch n := value.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(n))
		if err == nil {
			return parsed
		}
	}
	return 0
}
