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

func (p *orderedPolicies) Status() Status                                      { return Status{Enabled: true} }
func (p *orderedPolicies) Acquire(context.Context, string) (Attachment, error) { return p, nil }
func (*orderedPolicies) Enter(*exec.Cmd) error                                 { return nil }
func (*orderedPolicies) Gateway() string                                       { return "" }
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
