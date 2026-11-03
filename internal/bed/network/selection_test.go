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

// +case:id=network_selection_cause,desc=`A weaker network is selected after a classified failure`,expect=`Selection never rewrites the original error attribution`
func TestSelectedNetworkPreservesFailure(t *testing.T) {
	carrier := hostel.WrapError(hostel.ErrUnavailable, "IPv4 forwarding", hostel.CauseCarrier, errors.New("forwarding disabled"))
	bed := hostel.WrapError(hostel.ErrPreparationFailed, "namespace entry", hostel.CauseBed, errors.New("entry failed"))
	unknown := errors.New("composition failed")
	for _, tc := range []struct {
		name      string
		probe     Status
		selection error
		want      error
		cause     hostel.ErrorCause
	}{
		{"carrier probe survives selection", Status{Effective: Shared, Err: carrier}, unknown, carrier, hostel.CauseCarrier},
		{"typed composition failure", Status{Effective: Private}, bed, bed, hostel.CauseBed},
		{"unclassified composition failure", Status{Effective: Private}, unknown, unknown, hostel.CauseUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manager{config: Config{NetNS: tool.Auto, Level: Shared}, selectionErr: tc.selection, provider: reportedNetwork{report: tc.probe}}
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

func TestPrivateNetworkDoesNotRetainFailedCandidate(t *testing.T) {
	m := &Manager{config: Config{Level: Private}, selectionErr: errors.New("earlier attempt failed"), provider: reportedNetwork{report: Status{Effective: Private}}}
	if got := m.Status(); got.Err != nil || got.Cause != "" || got.Reason != "" {
		t.Fatalf("successful capability retained failure: %+v", got)
	}
}
