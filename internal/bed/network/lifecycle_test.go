package network

import (
	"context"
	"errors"
	"os/exec"
	"reflect"

	"github.com/qiankunli/hostel/protocol"
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

func (p *orderedPolicies) Status() hostnetwork.Status                          { return hostnetwork.Status{Available: true} }
func (p *orderedPolicies) Acquire(context.Context, string) (Attachment, error) { return p, nil }
func (*orderedPolicies) Enter(*exec.Cmd) error                                 { return nil }
func (*orderedPolicies) Gateway() string                                       { return "" }
func (*orderedPolicies) ResolverPath() string                                  { return "" }
func (*orderedPolicies) Close(context.Context) error                           { return nil }
func (p *orderedPolicies) NetworkPolicy(_ context.Context, _ string, m PolicyMutation) (Policy, error) {
	if p.calls.Add(1) == 1 {
		close(p.firstEntered)
		<-p.releaseFirst
	} else {
		close(p.secondEntered)
	}
	return *m.Replace, nil
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
	if got := b.Status().Network.Egress.Effective.DefaultAction; got != "deny" {
		t.Fatalf("stale publication: %s", got)
	}
	if err := m.Release(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if b.Status().Network.Egress.Effective != nil {
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
	if s.Isolation.Room != bed.Dorm || s.Egress.Effective != nil || s.Egress.Requested.DefaultAction != "deny" || s.Egress.State != "unenforced" {
		t.Fatalf("%+v", s)
	}
	updated, err := m.UpdatePolicy(t.Context(), b, PolicyMutation{Merge: []Rule{{Action: "allow", Target: "EXAMPLE.com"}}})
	if err != nil {
		t.Fatal(err)
	}
	updated.Requested.Egress[0].Target = "changed-response.example"

	if _, err := m.UpdatePolicy(t.Context(), b, PolicyMutation{Replace: &Policy{DefaultAction: "invalid"}}); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatal(err)
	}
	result, err := m.UpdatePolicy(t.Context(), b, PolicyMutation{})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "unenforced" || result.Requested.DefaultAction != "deny" || len(result.Requested.Egress) != 1 || result.Requested.Egress[0].Target != "example.com" {
		t.Fatalf("%+v", result)
	}
	result.Requested.Egress[0].Target = "mutated.example"
	again, _ := m.UpdatePolicy(t.Context(), b, PolicyMutation{})
	if again.Requested.Egress[0].Target != "example.com" {
		t.Fatal("published policy aliases owned state")
	}
	if _, err := m.UpdatePolicy(t.Context(), b, PolicyMutation{Remove: []string{"EXAMPLE.com"}}); err != nil {
		t.Fatal(err)
	}
	if got := b.Status().Network.Egress.Requested; len(got.Egress) != 0 {
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
	updateErr error
	updates   int
	current   Policy
	closeErr  error
	closed    bool
}

func (p *fakePolicyLease) Update(_ context.Context, change PolicyMutation) (Policy, error) {
	p.updates++
	if p.updateErr != nil {
		return Policy{}, p.updateErr
	}
	next, err := hostnetwork.ApplyMutation(p.current, change)
	if err != nil {
		return Policy{}, err
	}
	p.current = next
	return next, nil
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
	if backend.uid != 200001 || s.Isolation.Level != "shared" || s.Egress.Backend != "uid_nft" || s.Egress.State != "enforced" || s.Egress.Effective.DefaultAction != "deny" {
		t.Fatalf("UID=%d status=%+v", backend.uid, s)
	}
	result, err := m.UpdatePolicy(t.Context(), b, PolicyMutation{Merge: []Rule{{Action: "allow", Target: "example.com"}}})
	if err != nil || result.State != protocol.EgressEnforced || result.Scope != "bed_processes" || result.Backend != "uid_nft" {
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

// +case:id=egress_commit_evidence,desc=`UID policy update is rejected or loses its acknowledgement`,expect=`Keep the old effective policy only with unchanged evidence; GET neither calls backend nor repairs unknown state`
func TestEgressCommitEvidenceAndReadOnlySnapshot(t *testing.T) {
	owners := bed.NewOwners()
	b := bed.New("tenant", "", bed.Spec{})
	owners.Privilege.Set(b, bed.PrivilegeStatus{Assigned: true, UID: 200003})
	lease := &fakePolicyLease{}
	m := WithProvider(unavailableProvider{}, owners.Network)
	m.dedicatedUIDs, m.uidPolicies = true, &fakeUIDPolicies{lease: lease}
	if err := m.Prepare(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	defer m.Release(t.Context(), b)
	initial := b.Status().Network
	if initial.Isolation.Level != "shared" || initial.Isolation.Room != bed.Dorm || initial.Egress.Backend != "uid_nft" || initial.Egress.State != protocol.EgressEnforced || initial.Egress.Effective.DefaultAction != "allow" {
		t.Fatalf("default not published: %+v", initial)
	}
	// Default realization is reported before any policy GET or update.
	for range 3 {
		result, err := m.UpdatePolicy(t.Context(), b, PolicyMutation{})
		if err != nil || !reflect.DeepEqual(result, initial.Egress) {
			t.Fatalf("read: %+v %v", result, err)
		}
	}
	if lease.updates != 0 || !reflect.DeepEqual(initial, b.Status().Network) {
		t.Fatal("GET mutated backend or snapshot")
	}
	deny := PolicyMutation{Replace: &Policy{DefaultAction: "deny"}}
	lease.updateErr = &hostnetwork.PolicyUnchangedError{Err: errors.New("transaction rejected")}
	result, err := m.UpdatePolicy(t.Context(), b, deny)
	if err == nil || result.State != protocol.EgressEnforced || result.Effective.DefaultAction != "allow" || result.Requested.DefaultAction != "deny" || result.Diagnostic.Code != "policy_update_failed" {
		t.Fatalf("rejected update: %+v %v", result, err)
	}
	lease.updateErr = context.DeadlineExceeded
	result, err = m.UpdatePolicy(t.Context(), b, deny)
	if err == nil || result.State != protocol.EgressUnknown || result.Effective != nil {
		t.Fatalf("uncertain update: %+v %v", result, err)
	}
	before := b.Status().Network
	reads := lease.updates
	lease.updateErr = nil
	result, _ = m.UpdatePolicy(t.Context(), b, PolicyMutation{})
	if result.State != protocol.EgressUnknown || lease.updates != reads || !reflect.DeepEqual(before, b.Status().Network) {
		t.Fatal("GET repaired uncertain commit")
	}
	result, err = m.UpdatePolicy(t.Context(), b, PolicyMutation{Merge: []Rule{{Action: "allow", Target: "EXAMPLE.COM"}}})
	if err != nil || result.State != protocol.EgressEnforced || result.Effective.DefaultAction != "deny" || result.Diagnostic != nil {
		t.Fatalf("repair: %+v %v", result, err)
	}
	result.Effective.Egress[0].Target = "mutated.example"
	if b.Status().Network.Egress.Effective.Egress[0].Target != "example.com" {
		t.Fatal("response aliases status")
	}
	lease.closeErr = errors.New("journal removal failed after rules removed")
	if err := m.Release(t.Context(), b); err == nil {
		t.Fatal("release failure lost")
	}
	if s := b.Status().Network.Egress; s.State != protocol.EgressUnknown || s.Effective != nil || s.Diagnostic.Stage != "release" || m.allocation(b) == nil {
		t.Fatalf("partial release: %+v", s)
	}
	lease.closeErr = nil
	if err := m.Release(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if s := b.Status().Network; s.Egress.State != protocol.EgressReleased || s.Egress.Backend != "none" || s.Egress.Effective != nil || s.Isolation.Room != "" {
		t.Fatalf("released: %+v", s)
	}
}
