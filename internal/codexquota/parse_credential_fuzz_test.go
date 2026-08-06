package codexquota

import "testing"

func FuzzParseCredential(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"access_token":"fake-access-token","account_id":"account-test-1"}`),
		[]byte(`{"access_token":"fake-access-token","chatgpt_account_id":"account-test-2"}`),
		[]byte(`{"access_token":"fake-access-token","id_token":"eyJhbGciOiJub25lIn0.eyJjaGF0Z3B0X2FjY291bnRfaWQiOiJhY2NvdW50LXRlc3QtMyJ9."}`),
		[]byte(`{"access_token":"","account_id":"account-test-1"}`),
		[]byte(`{"id_token":"not-a-jwt"}`),
		[]byte(`{"access_token":"fake-access-token","id_token":"bad.payload"}`),
		[]byte(`{"access_token":`),
		[]byte(`{}`),
		[]byte(``),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_, _ = ParseCredential(data)
	})
}
