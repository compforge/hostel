package network

import (
	"context"
	"strings"
	"testing"

	"github.com/qiankunli/hostel/internal/feature"
)

func TestOffNetworkDoesNotConstructHostPool(t *testing.T) {
	m := NewConfigured(t.Context(), Config{NetNS: feature.Off})
	if m.pool != nil {
		t.Fatal("off network constructed a host pool")
	}
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := m.Status()
	if s.Enabled || s.Features["netns"].Probe != "not_probed" || s.Reason != "disabled_by_config" {
		t.Fatalf("%+v", s)
	}
	if a, err := m.Acquire(t.Context(), "bed"); err != nil || a != nil {
		t.Fatalf("%v %v", a, err)
	}
	if _, err := m.NetworkPolicy(t.Context(), "bed", PolicyMutation{}); err != ErrUnavailable {
		t.Fatal(err)
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type unavailableProvider struct{}

func (unavailableProvider) Acquire(context.Context, string) (Attachment, error) { return nil, nil }
func (unavailableProvider) NetworkPolicy(context.Context, string, PolicyMutation) (PolicyStatus, error) {
	return PolicyStatus{}, ErrUnavailable
}
func (unavailableProvider) Status() Status {
	return Status{Backend: "shared", Reason: "permission denied"}
}
func (unavailableProvider) Close(context.Context) error { return nil }
func TestRequiredNetworkFailsStartup(t *testing.T) {
	m := &Manager{config: Config{NetNS: feature.Required}, provider: unavailableProvider{}}
	if err := m.Start(t.Context()); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("%v", err)
	}
}
