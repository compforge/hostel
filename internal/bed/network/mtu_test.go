package network

import (
	"testing"

	"github.com/qiankunli/hostel/internal/bed/tool"
	"github.com/qiankunli/hostel/protocol"
)

// +case=mtu-startup-snapshot
// +spec=`MTU is observed even with netns disabled, and status reads never probe or expose mutable startup state.`
func TestMTUStartupSnapshot(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	m := NewConfigured(t.Context(), Config{NetNS: tool.Off, UIDNFT: tool.Off, MTU: 1300})
	if got := m.Status().MTU; got.Source != "not_probed" || got.Value != 0 {
		t.Fatalf("unstarted MTU = %+v", got)
	}
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	first := m.mtu.Load()
	if got := m.Status(); got.MTU.Value != 1300 || got.MTU.Source != "fallback" || got.MTU.Reason == "" || got.Isolation.Level != string(Shared) {
		t.Fatalf("disabled netns must still report MTU: %+v", got)
	}
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m.mtu.Load() != first {
		t.Fatal("startup repeated MTU discovery")
	}
	// A detected snapshot owns its interface slice; callers receive copies.
	m.mtu.Store(&protocol.NetworkMTUStatus{Value: 1500, Source: "detected", Interfaces: []protocol.NetworkMTUInterface{{Name: "eth0", MTU: 1500}}})
	got := m.Status()
	got.MTU.Interfaces[0].MTU = 9000
	got.MTU.Source = "fallback"
	if next := m.Status().MTU; next.Source != "detected" || next.Interfaces[0].MTU != 1500 {
		t.Fatalf("status exposed mutable MTU state: %+v", next)
	}
}
