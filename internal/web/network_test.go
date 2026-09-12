package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestNetworkDiagnosticsWithoutManager(t *testing.T) {
	s := newTestServer(t)
	for _, path := range []string{"/healthz", "/v1/status"} {
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

func TestNetworkPolicyUnavailableDoesNotPretendSuccess(t *testing.T) {
	s := newTestServer(t)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := do(t, s, method, "/v1/beds/missing/network/policy", strings.NewReader(`{}`), nil)
		// Missing beds must not be allocated by policy requests.
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s missing bed: %d %s", method, rec.Code, rec.Body)
		}
	}
	if _, err := s.mgr.Ensure(t.Context(), "default"); err != nil {
		t.Fatal(err)
	}
	rec := do(t, s, http.MethodPut, "/v1/beds/default/network/policy", strings.NewReader(`{"defaultAction":"deny"}`), nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled policy: %d %s", rec.Code, rec.Body)
	}
}
