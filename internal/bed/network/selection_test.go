package network

import (
	"errors"
	"testing"

	hostel "github.com/qiankunli/hostel/internal"
	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/tool"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

type reportedNetwork struct {
	unavailableProvider
	report hostnetwork.Status
}

func (p reportedNetwork) Status() hostnetwork.Status { return p.report }

// +case:id=network_selection_cause,desc=`Network probing fails and Auto uses shared networking`,expect=`The subsystem preserves the original failure attribution`
func TestSelectedNetworkPreservesFailure(t *testing.T) {
	carrier := hostel.WrapError(hostel.ErrUnavailable, "IPv4 forwarding", hostel.CauseCarrier, errors.New("forwarding disabled"))
	bed := hostel.WrapError(hostel.ErrPreparationFailed, "namespace entry", hostel.CauseBed, errors.New("entry failed"))
	unknown := errors.New("composition failed")
	for _, tc := range []struct {
		name  string
		probe hostnetwork.Status
		want  error
		cause hostel.ErrorCause
	}{
		{"carrier probe failure", hostnetwork.Status{Probe: hostnetwork.Probe{Err: carrier}}, carrier, hostel.CauseCarrier},
		{"namespace entry failure", hostnetwork.Status{Probe: hostnetwork.Probe{Err: bed}}, bed, hostel.CauseBed},
		{"unclassified probe failure", hostnetwork.Status{Probe: hostnetwork.Probe{Err: unknown}}, unknown, hostel.CauseUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manager{config: Config{NetNS: tool.Auto, MTU: hostnetwork.FallbackMTU}, provider: reportedNetwork{report: tc.probe}}
			if err := m.Start(t.Context()); err != nil {
				t.Fatalf("optional capability rejected startup: %v", err)
			}
			if attachment, err := m.Acquire(t.Context(), "tenant"); attachment != nil || err != nil {
				t.Fatalf("shared selection must remain usable: %v %v", attachment, err)
			}
			err := m.Status().Isolation.Probe.Err
			if !errors.Is(err, tc.want) || hostel.CauseOf(err) != tc.cause {
				t.Fatalf("lost failure: %v, cause=%s", err, hostel.CauseOf(err))
			}
		})
	}
}

func TestRequiredNetworkPreservesProbeError(t *testing.T) {
	original := hostel.WrapError(hostel.ErrUnavailable, "IPv4 forwarding", hostel.CauseCarrier, errors.New("forwarding disabled"))
	m := &Manager{config: Config{NetNS: tool.Required, MTU: hostnetwork.FallbackMTU}, provider: reportedNetwork{report: hostnetwork.Status{Probe: hostnetwork.Probe{Err: original}}}}
	err := m.Start(t.Context())
	if !errors.Is(err, original) || hostel.CauseOf(err) != hostel.CauseCarrier {
		t.Fatalf("required capability lost error: %v", err)
	}
}

func TestPrivateNetworkReportsRealizedCapability(t *testing.T) {
	m := &Manager{config: Config{MTU: hostnetwork.FallbackMTU}, provider: reportedNetwork{report: hostnetwork.Status{Available: true}}}
	if got := m.Status(); got.Isolation.Probe.Err != nil || got.Isolation.Diagnostic != nil {
		t.Fatalf("successful capability retained failure: %+v", got)
	}
}

func TestIsolationAndEgressHaveIndependentDiagnostics(t *testing.T) {
	owners := bed.NewOwners()
	forwarding := hostel.WrapError(hostel.ErrUnavailable, "forwarding disabled", hostel.CauseCarrier, nil)
	m := WithProvider(reportedNetwork{report: hostnetwork.Status{Reason: "forwarding disabled", Probe: hostnetwork.Probe{Stage: "forwarding", Err: forwarding}}}, owners.Network)
	m.dedicatedUIDs = true
	m.uidProbe = hostnetwork.Probe{Stage: "uid_rules", Error: "nft permission denied", Cause: hostel.CauseCarrier}
	s := m.Status()
	if s.Isolation.Level != "shared" || s.Isolation.Diagnostic.Stage != "forwarding" || s.Egress.Backend != "none" || s.Egress.Diagnostic.Stage != "uid_rules" || s.Egress.Diagnostic.Message != "nft permission denied" {
		t.Fatalf("mixed diagnoses: %+v", s)
	}
	b := bed.New("no-policy", "", bed.Spec{})
	if err := m.Prepare(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	defer m.Release(t.Context(), b)
	result := m.ReadPolicy(b)
	if result.State != "unenforced" || result.Effective != nil || result.Requested.DefaultAction != "allow" || result.Diagnostic.Message != "nft permission denied" {
		t.Fatalf("unenforced: %+v", result)
	}
	// Mutating a returned diagnostic must not corrupt subsequent status reads.
	result.Diagnostic.Message = "changed"
	if b.Status().Network.Egress.Diagnostic.Message != "nft permission denied" {
		t.Fatal("diagnostic aliases snapshot")
	}
	m.config.UIDNFT = tool.Off
	if d := m.Status().Egress.Diagnostic; d.Code != "disabled_by_config" || d.Cause != "request" {
		t.Fatalf("config disabled: %+v", d)
	}
}
