package web

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestNetworkDiagnosticsWithoutManager(t *testing.T) {
	s := newTestServer(t)
	for _, path := range []string{"/healthz", "/v1/diagnostics"} {
		rec := do(t, s, http.MethodGet, path, nil, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %s", path, rec.Body.String())
		}
		var body struct {
			Network struct {
				Enabled bool   `json:"enabled"`
				Backend string `json:"backend"`
				Reason  string `json:"reason"`
			} `json:"network"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Network.Enabled || body.Network.Backend != "shared" || body.Network.Reason == "" {
			t.Fatalf("unavailable network not explained: %s", rec.Body.String())
		}
	}
}
