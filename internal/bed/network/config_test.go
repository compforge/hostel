package network

import (
	"context"
	"reflect"
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
	if s.Enabled || s.Tools["netns"].Probe != "not_probed" || s.Reason != "disabled_by_config" {
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

func TestNetworkCapabilityFactsDoNotFollowSelection(t *testing.T) {
	m := &Manager{config: Config{Level: Shared}}
	s := m.describe(Status{Enabled: true, Backend: "netns", Scope: "bed_processes"})
	if s.Enabled || s.Effective != Shared || !reflect.DeepEqual(s.Supported, []Level{Shared, Private}) {
		t.Fatalf("profile hid host support or enabled unrequested isolation: %+v", s)
	}
	m.config = Config{Level: Private}
	strong := m.describe(Status{Enabled: true, Backend: "netns", Scope: "bed_processes"})
	if !strong.Enabled || !reflect.DeepEqual(strong.Supported, s.Supported) {
		t.Fatalf("selection changed capability list: %+v", strong)
	}
	next, ok := m.config.WithoutOptionalNamespace("combination failed")
	if !ok || next.NetNS != m.config.NetNS || next.Level != Shared {
		t.Fatalf("fallback changed tool policy: %+v", next)
	}
	if _, ok := next.WithoutOptionalNamespace("still failed"); ok {
		t.Fatal("fallback made no progress")
	}
	if _, ok := (Config{NetNS: tool.Required, Level: Private}).WithoutOptionalNamespace("failed"); ok {
		t.Fatal("dropped required namespace")
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
	m := &Manager{config: Config{NetNS: tool.Required}, provider: unavailableProvider{}}
	if err := m.Start(t.Context()); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("%v", err)
	}
}
