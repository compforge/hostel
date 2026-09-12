package bed

import (
	"sync"
	"testing"
)

func TestSnapshotsDoNotGrantWriteAccess(t *testing.T) {
	policy := &NetworkPolicy{DefaultAction: "deny", Egress: []NetworkRule{{Action: "allow", Target: "example.org"}}}
	spec := Spec{NetworkPolicy: policy, RecoveryDirs: []string{"original"}}
	b := New("a", "", spec)
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
	b := New("a", "", Spec{})
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
			owners.Store.Update(b, func(s *StoreStatus) { s.SnapshotGeneration++ })
			_ = b.Status()
		}
	}()
	wg.Wait()
	s := b.Status()
	if s.Privilege.UID != 1000 || s.Store.SnapshotGeneration != 1000 {
		t.Fatalf("lost domain update: %+v", s)
	}
}
func TestNameAndLocalIdentityAreDistinct(t *testing.T) {
	first := New("同名", "", Spec{})
	recovered := New(first.Name, first.ID, Spec{})
	replacement := New(first.Name, "", Spec{})
	if first.ID != recovered.ID || first.ID == replacement.ID || first.Name != replacement.Name {
		t.Fatal("name and local lifetime collapsed")
	}
}
