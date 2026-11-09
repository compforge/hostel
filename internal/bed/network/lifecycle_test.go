package network

import (
	"context"
	"errors"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

type orderedPolicies struct {
	firstEntered  chan struct{}
	releaseFirst  chan struct{}
	secondEntered chan struct{}
	calls         atomic.Int32
}

func (p *orderedPolicies) Status() Status                                      { return Status{Effective: Private} }
func (p *orderedPolicies) Acquire(context.Context, string) (Attachment, error) { return p, nil }
func (*orderedPolicies) Enter(*exec.Cmd) error                                 { return nil }
func (*orderedPolicies) Gateway() string                                       { return "" }
func (*orderedPolicies) ResolverPath() string                                  { return "" }
func (*orderedPolicies) Close(context.Context) error                           { return nil }
func (p *orderedPolicies) NetworkPolicy(_ context.Context, _ string, m PolicyMutation) (PolicyStatus, error) {
	if p.calls.Add(1) == 1 {
		close(p.firstEntered)
		<-p.releaseFirst
	} else {
		close(p.secondEntered)
	}
	return PolicyStatus{Policy: *m.Replace}, nil
}

func TestConcurrentPolicyPublicationFollowsAppliedOrder(t *testing.T) {
	p := &orderedPolicies{firstEntered: make(chan struct{}), releaseFirst: make(chan struct{}), secondEntered: make(chan struct{})}
	m := WithProvider(p, bed.NewOwners().Network)
	b := bed.New("same", "", bed.Spec{})
	if err := m.Prepare(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() {
		_, err := m.UpdatePolicy(t.Context(), b, PolicyMutation{Replace: &Policy{DefaultAction: "allow"}})
		done <- err
	}()
	<-p.firstEntered
	closeCtx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	if err := m.Release(closeCtx, b); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("release ignored pending update deadline: %v", err)
	}
	cancel()
	go func() {
		_, err := m.UpdatePolicy(t.Context(), b, PolicyMutation{Replace: &Policy{DefaultAction: "deny"}})
		done <- err
	}()
	select {
	case <-p.secondEntered:
		t.Error("second update overtook pending publication")
	case <-time.After(20 * time.Millisecond):
	}
	close(p.releaseFirst)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if got := b.Status().Network.Policy.DefaultAction; got != "deny" {
		t.Fatalf("stale publication: %s", got)
	}
	if err := m.Release(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if b.Status().Network.Policy != nil {
		t.Fatal("released allocation retained policy")
	}
}

func TestSharedNetworkPreservesDesiredPolicyWithoutClaimingEnforcement(t *testing.T) {
	m := WithProvider(unavailableProvider{}, bed.NewOwners().Network)
	b := bed.New("tenant", "", bed.Spec{NetworkPolicy: &bed.NetworkPolicy{DefaultAction: "deny"}})
	if err := m.Prepare(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	defer m.Release(t.Context(), b)
	s := b.Status().Network
	if s.Room != bed.Dorm || s.Policy != nil || s.RequestedPolicy.DefaultAction != "deny" || s.PolicyStatus != "unenforced" {
		t.Fatalf("%+v", s)
	}
	updated, err := m.UpdatePolicy(t.Context(), b, PolicyMutation{Merge: []Rule{{Action: "allow", Target: "EXAMPLE.com"}}})
	if err != nil {
		t.Fatal(err)
	}
	updated.RequestedPolicy.Egress[0].Target = "changed-response.example"

	if _, err := m.UpdatePolicy(t.Context(), b, PolicyMutation{Replace: &Policy{DefaultAction: "invalid"}}); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatal(err)
	}
	result, err := m.UpdatePolicy(t.Context(), b, PolicyMutation{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "unenforced" || result.RequestedPolicy.DefaultAction != "deny" || len(result.RequestedPolicy.Egress) != 1 || result.RequestedPolicy.Egress[0].Target != "example.com" {
		t.Fatalf("%+v", result)
	}
	result.RequestedPolicy.Egress[0].Target = "mutated.example"
	again, _ := m.UpdatePolicy(t.Context(), b, PolicyMutation{})
	if again.RequestedPolicy.Egress[0].Target != "example.com" {
		t.Fatal("published policy aliases owned state")
	}
	if _, err := m.UpdatePolicy(t.Context(), b, PolicyMutation{Remove: []string{"EXAMPLE.com"}}); err != nil {
		t.Fatal(err)
	}
	if got := b.Status().Network.RequestedPolicy; len(got.Egress) != 0 {
		t.Fatalf("delete not reflected: %+v", got)
	}
}

type fakeUIDPolicies struct {
	lease      *fakePolicyLease
	uid        uint32
	acquireErr error
}

func (p *fakeUIDPolicies) Acquire(_ context.Context, _ string, uid uint32) (hostnetwork.PolicyLease, error) {
	p.uid = uid
	return p.lease, p.acquireErr
}
func (*fakeUIDPolicies) Close(context.Context) error { return nil }

type fakePolicyLease struct {
	current  Policy
	closeErr error
	closed   bool
}

func (p *fakePolicyLease) Update(_ context.Context, change PolicyMutation) (PolicyStatus, error) {
	next, err := hostnetwork.ApplyMutation(p.current, change)
	if err != nil {
		return PolicyStatus{}, err
	}
	p.current = next
	return PolicyStatus{Status: "ok", Mode: "enforcing", EnforcementMode: "dns+nft", Policy: next}, nil
}
func (p *fakePolicyLease) Close(context.Context) error {
	if p.closeErr != nil {
		return p.closeErr
	}
	p.closed = true
	return nil
}

func TestSharedNetworkEnforcesPolicyWithAllocatedUID(t *testing.T) {
	owners := bed.NewOwners()
	b := bed.New("tenant", "", bed.Spec{NetworkPolicy: &bed.NetworkPolicy{DefaultAction: "deny"}})
	owners.Privilege.Set(b, bed.PrivilegeStatus{Assigned: true, UID: 200001, GID: 200001})
	lease := &fakePolicyLease{}
	backend := &fakeUIDPolicies{lease: lease}
	m := WithProvider(unavailableProvider{}, owners.Network)
	m.dedicatedUIDs, m.uidPolicies = true, backend
	if err := m.Prepare(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	s := b.Status().Network
	if backend.uid != 200001 || s.Level != "shared" || s.PolicyBackend != "uid" || s.PolicyStatus != "enforced" || s.Policy.DefaultAction != "deny" {
		t.Fatalf("UID=%d status=%+v", backend.uid, s)
	}
	result, err := m.UpdatePolicy(t.Context(), b, PolicyMutation{Merge: []Rule{{Action: "allow", Target: "example.com"}}})
	if err != nil || result.Status != "ok" || result.Scope != "bed_processes" || result.EnforcementMode != "dns+nft" {
		t.Fatalf("%+v %v", result, err)
	}
	lease.closeErr = errors.New("cleanup failed")
	if err := m.Release(t.Context(), b); err == nil || m.allocation(b) == nil {
		t.Fatal("failed cleanup lost allocation")
	}
	lease.closeErr = nil
	if err := m.Release(t.Context(), b); err != nil || !lease.closed || m.allocation(b) != nil {
		t.Fatalf("release: %v", err)
	}
}

func TestUIDPolicyFailedPreparationRetainsCleanupOwner(t *testing.T) {
	owners := bed.NewOwners()
	b := bed.New("tenant", "", bed.Spec{})
	owners.Privilege.Set(b, bed.PrivilegeStatus{Assigned: true, UID: 200002})
	lease := &fakePolicyLease{}
	m := WithProvider(unavailableProvider{}, owners.Network)
	m.dedicatedUIDs = true
	m.uidPolicies = &fakeUIDPolicies{lease: lease, acquireErr: errors.New("nft failed")}
	if err := m.Prepare(t.Context(), b); err == nil {
		t.Fatal("failed installation admitted Bed")
	}
	if err := m.Release(t.Context(), b); err != nil || !lease.closed {
		t.Fatalf("partial allocation not cleaned: %v", err)
	}
}
