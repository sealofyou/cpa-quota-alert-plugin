package management

import (
	"encoding/json"
	"strings"
	"testing"
)

func FuzzDecodeRequest(f *testing.F) {
	official, err := json.Marshal(Request{
		HostCallbackID: "host-test",
		Method:         "POST",
		Path:           "/cpa-quota-alert/check",
		Headers:        map[string][]string{},
		Query:          map[string][]string{},
		Body:           []byte(`{}`),
	})
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range [][]byte{
		official,
		[]byte(`{"Method":"GET","Path":"/cpa-quota-alert/status","Body":""}`),
		[]byte(`{"Method":"POST","Path":"/cpa-quota-alert/check","Body":"e30="}`),
		[]byte(`{"Method":"POST","Path":"/cpa-quota-alert/check","unknown":true}`),
		[]byte(`{"Method":"POST","Path":"/cpa-quota-alert/check","Body":{`),
		[]byte(strings.Repeat("x", MaxManagementRequestBytes+1)),
		[]byte(``),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxManagementRequestBytes+1 {
			t.Skip()
		}
		_, _ = decodeRequest(data)
	})
}
