package network

import (
	"errors"
	"testing"

	hostel "github.com/qiankunli/hostel/internal"
	"github.com/qiankunli/hostel/internal/bed/tool"
)

type reportedNetwork struct {
	unavailableProvider
	report Status
}

func (p reportedNetwork) Status() Status { return p.report }

// +case:id=network_selection_cause,desc=`Network probing fails and Auto uses shared networking`,expect=`The subsystem preserves the original failure attribution`
func TestSelectedNetworkPreservesFailure(t *testing.T) {
	carrier := hostel.WrapError(hostel.ErrUnavailable, "IPv4 forwarding", hostel.CauseCarrier, errors.New("forwarding disabled"))
	bed := hostel.WrapError(hostel.ErrPreparationFailed, "namespace entry", hostel.CauseBed, errors.New("entry failed"))
	unknown := errors.New("composition failed")
	for _, tc := range []struct {
		name  string
		probe Status
		want  error
		cause hostel.ErrorCause
	}{
		{"carrier probe failure", Status{Effective: Shared, Err: carrier}, carrier, hostel.CauseCarrier},
		{"namespace entry failure", Status{Effective: Shared, Err: bed}, bed, hostel.CauseBed},
		{"unclassified probe failure", Status{Effective: Shared, Err: unknown}, unknown, hostel.CauseUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manager{config: Config{NetNS: tool.Auto}, provider: reportedNetwork{report: tc.probe}}
			if err := m.Start(t.Context()); err != nil {
				t.Fatalf("optional capability rejected startup: %v", err)
			}
			if attachment, err := m.Acquire(t.Context(), "tenant"); attachment != nil || err != nil {
				t.Fatalf("shared selection must remain usable: %v %v", attachment, err)
			}
			err := m.UnavailableError()
			if !errors.Is(err, tc.want) || !errors.Is(err, ErrUnavailable) || hostel.CauseOf(err) != tc.cause {
				t.Fatalf("lost failure: %v, cause=%s", err, hostel.CauseOf(err))
			}
		})
	}
}

func TestRequiredNetworkPreservesProbeError(t *testing.T) {
	original := hostel.WrapError(hostel.ErrUnavailable, "IPv4 forwarding", hostel.CauseCarrier, errors.New("forwarding disabled"))
	m := &Manager{config: Config{NetNS: tool.Required}, provider: reportedNetwork{report: Status{Effective: Shared, Err: original}}}
	err := m.Start(t.Context())
	if !errors.Is(err, original) || hostel.CauseOf(err) != hostel.CauseCarrier {
		t.Fatalf("required capability lost error: %v", err)
	}
}

func TestPrivateNetworkReportsRealizedCapability(t *testing.T) {
	m := &Manager{config: Config{}, provider: reportedNetwork{report: Status{Effective: Private}}}
	if got := m.Status(); got.Err != nil || got.Cause != "" || got.Reason != "" {
		t.Fatalf("successful capability retained failure: %+v", got)
	}
}
