package network

import (
	"context"
	"errors"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed"
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
