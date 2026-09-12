//go:build linux

package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/network"
)

func TestLinuxNetworkPolicyAPI(t *testing.T) {
	if os.Getenv("HOSTEL_NETWORK_TEST") != "enabled" {
		t.Skip("requires disposable Linux container")
	}
	s := newTestServer(t)
	manager := network.New(t.Context())
	if err := manager.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	if !manager.Diagnostics().Enabled {
		t.Fatalf("network unavailable: %+v", manager.Diagnostics())
	}
	s.mgr.SetNetworkManager(manager)
	rec := do(t, s, http.MethodPost, "/v1/beds", strings.NewReader(`{"id":"policy-a","sync":"noop","networkPolicy":{"defaultAction":"deny"}}`), nil)
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := s.mgr.Ensure(ctx, "policy-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.mgr.Ensure(ctx, "policy-b"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, body string }{
		{http.MethodGet, ""},
		{http.MethodPut, `{"defaultAction":"deny","egress":[{"action":"allow","target":"example.com"}]}`},
		{http.MethodPatch, `[{"action":"deny","target":"169.254.169.254"}]`},
		{http.MethodDelete, `["example.com"]`},
	} {
		rec = do(t, s, tc.method, "/v1/beds/policy-a/network/policy", strings.NewReader(tc.body), map[string]string{"X-Hostel-Bed": "policy-b"})
		if rec.Code != http.StatusOK {
			t.Fatalf("%s policy: %d %s", tc.method, rec.Code, rec.Body)
		}
	}
	rec = do(t, s, http.MethodGet, "/v1/beds/policy-a/network/policy", nil, nil)
	var a network.PolicyStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if a.Policy.DefaultAction != "deny" || len(a.Policy.Egress) != 1 || a.Policy.Egress[0].Target != "169.254.169.254" || a.Scope != "bed_processes" {
		t.Fatalf("policy A: %+v", a)
	}
	rec = do(t, s, http.MethodGet, "/v1/beds/policy-b/network/policy", nil, nil)
	var b network.PolicyStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.Policy.DefaultAction != "allow" || len(b.Policy.Egress) != 0 {
		t.Fatalf("A API changed B: %+v", b)
	}
}
