package bed

import (
	"sync"
	"testing"
)

func TestSnapshotsDoNotGrantWriteAccess(t *testing.T) {
	policy := &NetworkPolicy{DefaultAction: "deny", Egress: []NetworkRule{{Action: "allow", Target: "example.org"}}}
	spec := Spec{NetworkPolicy: policy, RecoveryDirs: []string{"original"}}
	b := New("a", 0, spec)
	policy.Egress[0].Target = "mutated-input"
	spec.RecoveryDirs[0] = "mutated-input"
	snapshot := b.Spec()
	snapshot.NetworkPolicy.Egress[0].Target = "mutated-read"
	snapshot.RecoveryDirs[0] = "mutated-read"
	if got := b.Spec(); got.NetworkPolicy.Egress[0].Target != "example.org" || got.RecoveryDirs[0] != "original" {
		t.Fatalf("spec aliased: %+v", got)
	}
	owners := NewOwners()
	var retained *NetworkStatus
	owners.Network.Update(b, func(s *NetworkStatus) {
		s.Policy = &NetworkPolicy{Egress: []NetworkRule{{Target: "original"}}}
		retained = s
	})
	retained.Policy.Egress[0].Target = "mutated-callback"
	status := b.Status()
	status.Network.Policy.Egress[0].Target = "mutated-read"
	if got := b.Status().Network.Policy.Egress[0].Target; got != "original" {
		t.Fatalf("status aliased: %s", got)
	}
}
func TestDomainWritersPreserveConcurrentSections(t *testing.T) {
	b := New("a", 0, Spec{})
	owners := NewOwners()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			owners.Privilege.Update(b, func(s *PrivilegeStatus) { s.UID++ })
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			owners.Store.Update(b, func(s *StoreStatus) { s.Generation++ })
			_ = b.Status()
		}
	}()
	wg.Wait()
	s := b.Status()
	if s.Privilege.UID != 1000 || s.Store.Generation != 1000 {
		t.Fatalf("lost domain update: %+v", s)
	}
}
func TestAllocationAndRetainedIdentityAreDistinct(t *testing.T) {
	local := New("same", 0, Spec{})
	first := New("same", local.LocalID, Spec{})
	next := New("same", local.LocalID, Spec{})
	replacement := New("same", 0, Spec{})
	if first.InstanceID == next.InstanceID || first.LocalID != next.LocalID || replacement.LocalID == first.LocalID {
		t.Fatal("identity lifetimes collapsed")
	}
}
