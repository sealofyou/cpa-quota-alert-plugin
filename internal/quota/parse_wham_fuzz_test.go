package quota

import "testing"

func FuzzParseWham(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"plan_type":"plus","rate_limit":{"used_percent":25,"limit_window_seconds":604800}}`),
		[]byte(`{"planType":"team","rateLimit":{"primary":{"usedPercent":10},"secondary":{"used_percent":20,"limit_window_seconds":604800}}}`),
		[]byte(`{"plan_type":"plus","rate_limit":{"primary":{"used_percent":30},"secondary":{"used_percent":40,"durationSeconds":604800}}}`),
		[]byte(`{"plan_type":"plus","rate_limit":{"secondary":{"limit_window_seconds":604800,"allowed":false,"reset_seconds":60}}}`),
		[]byte(`{"plan_type":"plus","rate_limit":{"secondary":{"used_percent":50,"limit_window_seconds":123}}}`),
		[]byte(`{"plan_type":"plus","rate_limit":{"secondary":{"limit_window_seconds":604800}}}`),
		[]byte(`{"plan_type":`),
		[]byte(`[]`),
		[]byte(``),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_, _ = ParseWham(data)
	})
}
