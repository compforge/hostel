package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	hostel "github.com/qiankunli/hostel/internal"
	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/network"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
	"github.com/qiankunli/hostel/protocol"
)

type failedNetworkProbe struct{ cause hostel.ErrorCause }

func (p failedNetworkProbe) Status() hostnetwork.Status {
	return hostnetwork.Status{Reason: "carrier prerequisite unavailable", Probe: hostnetwork.Probe{Err: hostel.WrapError(hostel.ErrUnavailable, "carrier prerequisite", p.cause, nil), Stage: "forwarding", Cause: p.cause}}
}
func (failedNetworkProbe) Acquire(context.Context, string) (network.Attachment, error) {
	panic("unavailable backend must not allocate")
}
func (failedNetworkProbe) NetworkPolicy(context.Context, string, network.PolicyMutation) (network.Policy, error) {
	panic("unavailable backend must not apply policy")
}
func (failedNetworkProbe) Close(context.Context) error { return nil }

// +case:id=network_cause_http,desc=`Create Bed with policy against unavailable carrier or Bed mechanism`,expect=`Dorm admits with an unenforced policy with its own identity diagnostic; invalid policy remains request`
func TestNetworkCauseAcrossStatusAndCreate(t *testing.T) {
	for _, cause := range []hostel.ErrorCause{hostel.CauseCarrier, hostel.CauseBed, hostel.CauseUnknown} {
		t.Run(string(cause), func(t *testing.T) {
			s := newTestServer(t)
			s.mgr.SetNetworkManager(network.WithProvider(failedNetworkProbe{cause: cause}, model.StatusWriter[model.NetworkStatus]{}))
			rec := do(t, s, http.MethodGet, "/v1/beds/capabilities", nil, nil)
			var capability struct {
				Network network.Status `json:"network"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &capability); err != nil {
				t.Fatal(err)
			}
			if rec.Code != 200 || capability.Network.Isolation.Diagnostic.Cause != string(cause) {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			body := `{"id":"policy-probe","networkPolicy":{"defaultAction":"deny"},"pillows":[{"name":"main","mounts":[{"volume":"workspace","path":"/workspace"}]}]}`
			rec = do(t, s, http.MethodPost, "/v1/beds", strings.NewReader(body), nil)
			if rec.Code != http.StatusAccepted && rec.Code != http.StatusOK {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if _, err := s.mgr.Ensure(t.Context(), "policy-probe"); err != nil {
				t.Fatal(err)
			}
			rec = do(t, s, http.MethodGet, "/v1/beds/policy-probe/network/policy", nil, nil)
			var policy protocol.EgressStatus
			if err := json.Unmarshal(rec.Body.Bytes(), &policy); err != nil {
				t.Fatal(err)
			}
			if rec.Code != 200 || policy.Diagnostic.Code != "dedicated_identity_unavailable" || policy.State != "unenforced" || policy.Effective != nil || policy.Requested == nil || policy.Requested.DefaultAction != "deny" {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			var response struct {
				Cause hostel.ErrorCause `json:"cause"`
			}
			rec = do(t, s, http.MethodPost, "/v1/beds", strings.NewReader(strings.Replace(body, `"deny"`, `"invalid"`, 1)), nil)
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if rec.Code != 400 || response.Cause != hostel.CauseRequest {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
}
