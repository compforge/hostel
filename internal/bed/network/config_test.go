package network

import (
	"context"
	"errors"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
	"strings"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/tool"
)

func TestOffNetworkDoesNotConstructHostPool(t *testing.T) {
	m := NewConfigured(t.Context(), Config{NetNS: tool.Off})
	if m.pool != nil {
		t.Fatal("off network constructed a host pool")
	}
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := m.Status()
	if s.Isolation.Level == string(Private) || s.Tools["netns"].Probe != "not_probed" || s.Isolation.Diagnostic.Code != "disabled_by_config" {
		t.Fatalf("%+v", s)
	}
	if a, err := m.Acquire(t.Context(), "bed"); err != nil || a != nil {
		t.Fatalf("%v %v", a, err)
	}
	if _, err := m.NetworkPolicy(t.Context(), "bed", PolicyMutation{}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type unavailableProvider struct{}

func (unavailableProvider) Acquire(context.Context, string) (Attachment, error) { return nil, nil }
func (unavailableProvider) NetworkPolicy(context.Context, string, PolicyMutation) (Policy, error) {
	return Policy{}, ErrUnavailable
}
func (unavailableProvider) Status() hostnetwork.Status {
	return hostnetwork.Status{Reason: "permission denied"}
}
func (unavailableProvider) Close(context.Context) error { return nil }
func TestRequiredNetworkFailsStartup(t *testing.T) {
	m := &Manager{config: Config{NetNS: tool.Required}, provider: unavailableProvider{}}
	if err := m.Start(t.Context()); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("%v", err)
	}
}

func TestUIDPolicyToolSelectionRequiresActualIdentity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		policy    tool.Policy
		dedicated bool
		wantError bool
	}{
		{"disabled", tool.Off, true, false},
		{"shared identity", tool.Auto, false, false},
		{"required without identity", tool.Required, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewConfigured(t.Context(), Config{NetNS: tool.Off, UIDNFT: tc.policy})
			m.SetDedicatedUIDs(tc.dedicated)
			err := m.Start(t.Context())
			if (err != nil) != tc.wantError {
				t.Fatalf("start: %v", err)
			}
			if err != nil && !strings.Contains(err.Error(), "network.uid_nft required") {
				t.Fatal(err)
			}
			s := m.Status()
			if s.Egress.Backend != "none" || s.Tools["uid_nft"].Selected || s.Tools["uid_nft"].Probe != "not_probed" {
				t.Fatalf("%+v", s)
			}
		})
	}
}
