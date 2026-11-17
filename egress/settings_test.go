package egress

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIPv6PatchPreservesProxySettings(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var patch proxyPatch
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
				t.Error(err)
			}
			if patch.AllowIPv6 == nil || *patch.AllowIPv6 != allowed || patch.Execution != nil || patch.LLM != nil || patch.Search != nil {
				t.Errorf("IPv6 patch changes other settings or loses false: %+v", patch)
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		client := &Client{base: gateway.URL, http: gateway.Client()}
		if err := client.ApplyIPv6(allowed); err != nil {
			t.Fatal(err)
		}
		gateway.Close()
	}
}
