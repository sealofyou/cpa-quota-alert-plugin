package config

import "testing"

func FuzzParseYAML(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(``),
		[]byte("dry_run: true\nconcurrency: 2\n"),
		[]byte("plan_rules:\n  - name: plus\n    aliases: [plus]\n    window: 7d\n    weight: 1\n"),
		[]byte("smtp:\n  enabled: true\n  host: mail.test\n  port: 587\n  username_env: SMTP_USER\n  password_env: SMTP_PASS\n  recipients_env: SMTP_TO\n  from_env: SMTP_FROM\n"),
		[]byte("webhook:\n  enabled: true\n  url_env: WEBHOOK_URL\n  auth_header_env: WEBHOOK_AUTH\n"),
		[]byte("a: 1\na: 2\n"),
		[]byte("1: value\n"),
		[]byte("value: [\n"),
		[]byte("---\na: 1\n---\nb: 2\n"),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxYAMLBytes {
			t.Skip()
		}
		_, _ = ParseYAML(data, func(string) string { return "safe-placeholder" })
	})
}
