package network

import (
	"context"
	"errors"
	"fmt"
	"log"

	hostel "github.com/qiankunli/hostel/internal"
	"github.com/qiankunli/hostel/internal/bed"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
	"github.com/qiankunli/hostel/protocol"
	"golang.org/x/sync/semaphore"
)

// Policy application and publication share this allocation lock, so concurrent
// updates cannot publish an older policy after a newer one has taken effect.
type bedAllocation struct {
	mu         *semaphore.Weighted
	attachment Attachment
	policy     hostnetwork.PolicyLease
	released   bool
}

type Provider interface {
	Acquire(context.Context, string) (Attachment, error)
	NetworkPolicy(context.Context, string, PolicyMutation) (Policy, error)
	Status() hostnetwork.Status
	Close(context.Context) error
}

// WithProvider supplies the namespace implementation; useful for capability
// fallbacks and deterministic lifecycle tests without host mutation.
func WithProvider(p Provider, status bed.StatusWriter[bed.NetworkStatus]) *Manager {
	return &Manager{config: Config{MTU: hostnetwork.FallbackMTU}, provider: p, status: status}
}
func (m *Manager) SetStatusWriter(status bed.StatusWriter[bed.NetworkStatus]) { m.status = status }

// +spec=`Publish actual isolation and egress independently; no Bed is ready before its initial policy has been evaluated.`
func (m *Manager) Prepare(ctx context.Context, b *bed.Bed) (err error) {
	desired := Policy{DefaultAction: "allow"}
	if initial := b.Spec().NetworkPolicy; initial != nil {
		desired = FromModel(*initial)
	}
	desired, err = NormalizePolicy(desired)
	if err != nil {
		return err
	}
	m.status.Set(b, bed.NetworkStatus{Egress: protocol.EgressStatus{Backend: "none", State: protocol.EgressPending, Scope: "bed_processes", Requested: ToModel(desired)}})
	stage := "isolation"
	defer func() {
		if err != nil {
			m.preparationFailed(b, stage, err)
		}
	}()
	attachment, err := m.Acquire(ctx, b.ID.String())
	if err != nil {
		return err
	}
	a := &bedAllocation{mu: semaphore.NewWeighted(1), attachment: attachment}
	// The new semaphore is uncontended; acquire before publishing the owner so
	// release and updates wait for preparation, including partial UID allocation.
	_ = a.mu.Acquire(context.Background(), 1)
	defer a.mu.Release(1)
	m.hookMu.Lock()
	if m.allocations == nil {
		m.allocations = make(map[*bed.Bed]*bedAllocation)
	}
	m.allocations[b] = a
	m.hookMu.Unlock()
	report := m.Status()
	level := Shared
	if a.attachment != nil {
		level = Private
	}
	if report.Isolation.Level == string(Private) && a.attachment == nil {
		return fmt.Errorf("private network allocation returned no attachment")
	}
	m.status.Update(b, func(s *bed.NetworkStatus) {
		s.Isolation = protocol.NetworkIsolationStatus{Level: string(level), Room: level.Room(), Diagnostic: report.Isolation.Diagnostic}
		if a.attachment != nil {
			s.Isolation.Gateway = a.attachment.Gateway()
		}
	})
	stage = "egress"
	if a.attachment == nil && m.uidPolicies != nil {
		identity := b.Status().Privilege
		if !m.dedicatedUIDs || !identity.Assigned {
			return fmt.Errorf("UID network policy requires a prepared dedicated identity")
		}
		a.policy, err = m.uidPolicies.Acquire(ctx, b.ID.String(), identity.UID)
		if a.policy != nil {
			m.status.Update(b, func(s *bed.NetworkStatus) { s.Egress.Backend = "uid_nft" })
		}
		if err != nil {
			return fmt.Errorf("prepare UID network policy: %w", err)
		}
		if a.policy == nil {
			return fmt.Errorf("UID network allocation returned no policy lease")
		}
	}
	// Successful acquisition installs the backend's default allow policy. Reads
	// never need to call the backend to discover or publish this initial state.
	m.status.Update(b, func(s *bed.NetworkStatus) {
		switch {
		case a.attachment != nil:
			s.Egress.Backend = "netns_nft"
		case a.policy != nil:
			s.Egress.Backend = "uid_nft"
		default:
			s.Egress.State, s.Egress.Diagnostic = protocol.EgressUnenforced, report.Egress.Diagnostic
			return
		}
		s.Egress.State = protocol.EgressEnforced
		s.Egress.Effective = ToModel(Policy{DefaultAction: "allow"})
	})
	if b.Spec().NetworkPolicy != nil {
		_, err = m.updatePolicy(ctx, b, a, PolicyMutation{Replace: &desired})
	}
	return err
}

func (m *Manager) preparationFailed(b *bed.Bed, stage string, err error) {
	m.status.Update(b, func(s *bed.NetworkStatus) {
		if stage == "isolation" {
			s.Isolation.Diagnostic = operationDiagnostic("isolation_prepare_failed", "prepare", err)
		}
		// An update already published a more precise result (possibly the old policy).
		if s.Egress.Diagnostic == nil {
			s.Egress.State, s.Egress.Effective = protocol.EgressUnknown, nil
			s.Egress.Diagnostic = operationDiagnostic("egress_prepare_failed", "prepare", err)
		}
	})
	log.Printf("network: bed=%s stage=%s preparation failed: %v", b.ID.String(), stage, err)
}

func (m *Manager) Attachment(b *bed.Bed) Attachment {
	if a := m.allocation(b); a != nil {
		return a.attachment
	}
	return nil
}
func (m *Manager) allocation(b *bed.Bed) *bedAllocation {
	m.hookMu.Lock()
	defer m.hookMu.Unlock()
	return m.allocations[b]
}
func (m *Manager) SetPortMappings(ports *PortMappings) { m.portMappings = ports }

func (m *Manager) Release(ctx context.Context, b *bed.Bed) error {
	if err := m.portMappings.CheckReleased(b); err != nil {
		return err
	}
	a := m.allocation(b)
	if a == nil {
		return nil
	}
	if err := a.mu.Acquire(ctx, 1); err != nil {
		return err
	}
	defer a.mu.Release(1)
	if a.released {
		return nil
	}
	if a.policy != nil {
		if err := a.policy.Close(ctx); err != nil {
			m.releaseFailed(b, err)
			return err
		}
	}
	if a.attachment != nil {
		if err := a.attachment.Close(ctx); err != nil {
			m.status.Update(b, func(s *bed.NetworkStatus) {
				s.Isolation = protocol.NetworkIsolationStatus{Diagnostic: operationDiagnostic("isolation_release_failed", "release", err)}
				s.Isolation.Gateway = ""
			})
			m.releaseFailed(b, err)
			return err
		}
	}
	a.released = true
	m.hookMu.Lock()
	delete(m.allocations, b)
	m.hookMu.Unlock()
	m.status.Update(b, func(s *bed.NetworkStatus) {
		s.Isolation, s.Isolation.Gateway = protocol.NetworkIsolationStatus{}, ""
		s.Egress.Backend, s.Egress.State = "none", protocol.EgressReleased
		s.Egress.Effective, s.Egress.Diagnostic = nil, nil
	})
	log.Printf("network: bed=%s state=released", b.ID.String())
	return nil
}
func ToModel(p Policy) *bed.NetworkPolicy {
	result := &bed.NetworkPolicy{DefaultAction: p.DefaultAction}
	for _, r := range p.Egress {
		result.Egress = append(result.Egress, bed.NetworkRule{Action: r.Action, Target: r.Target})
	}
	return result
}
func FromModel(p bed.NetworkPolicy) Policy {
	result := Policy{DefaultAction: p.DefaultAction}
	for _, r := range p.Egress {
		result.Egress = append(result.Egress, Rule{Action: r.Action, Target: r.Target})
	}
	return result
}

// ReadPolicy returns a detached snapshot without probing or mutating a backend.
func (m *Manager) ReadPolicy(b *bed.Bed) protocol.EgressStatus {
	return b.Status().Network.Egress
}

func (m *Manager) UpdatePolicy(ctx context.Context, b *bed.Bed, mutation PolicyMutation) (protocol.EgressStatus, error) {
	if mutation.Replace == nil && mutation.Merge == nil && mutation.Remove == nil {
		return m.ReadPolicy(b), nil
	}
	a := m.allocation(b)
	if a == nil {
		return protocol.EgressStatus{}, m.UnavailableError()
	}
	if err := a.mu.Acquire(ctx, 1); err != nil {
		return protocol.EgressStatus{}, err
	}
	defer a.mu.Release(1)
	if a.released {
		return protocol.EgressStatus{}, m.UnavailableError()
	}
	return m.updatePolicy(ctx, b, a, mutation)
}

// +spec=`Rejected input leaves desired state unchanged; failed realization never publishes the new policy as effective. Unknown commits cannot be repaired by a GET.`
// +link=`docs/network.md#状态契约`
func (m *Manager) updatePolicy(ctx context.Context, b *bed.Bed, a *bedAllocation, mutation PolicyMutation) (protocol.EgressStatus, error) {
	previous := b.Status().Network.Egress
	current := Policy{DefaultAction: "allow"}
	if previous.Requested != nil {
		current = FromModel(*previous.Requested)
	}
	desired, err := hostnetwork.ApplyMutation(current, mutation)
	if err != nil {
		return protocol.EgressStatus{}, err
	}
	// Always replace with the complete accepted target, including after a failed
	// update; a backend's last committed policy may lag behind requested state.
	m.status.Update(b, func(s *bed.NetworkStatus) { s.Egress.Requested = ToModel(desired) })
	if a.attachment == nil && a.policy == nil {
		log.Printf("network: bed=%s state=unenforced backend=none rules=%d", b.ID.String(), len(desired.Egress))
		return m.ReadPolicy(b), nil
	}
	change := PolicyMutation{Replace: &desired}
	if a.policy != nil {
		_, err = a.policy.Update(ctx, change)
	} else {
		_, err = m.NetworkPolicy(ctx, b.ID.String(), change)
	}
	m.status.Update(b, func(s *bed.NetworkStatus) {
		if err == nil {
			s.Egress.State, s.Egress.Effective, s.Egress.Diagnostic = protocol.EgressEnforced, ToModel(desired), nil
			return
		}
		s.Egress.Diagnostic = operationDiagnostic("policy_update_failed", "update", err)
		var unchanged *hostnetwork.PolicyUnchangedError
		if !errors.As(err, &unchanged) || previous.State != protocol.EgressEnforced {
			s.Egress.State, s.Egress.Effective = protocol.EgressUnknown, nil
		}
	})
	result := m.ReadPolicy(b)
	log.Printf("network: bed=%s state=%s backend=%s rules=%d error=%v", b.ID.String(), result.State, result.Backend, len(desired.Egress), err)
	return result, err
}

func (m *Manager) releaseFailed(b *bed.Bed, err error) {
	m.status.Update(b, func(s *bed.NetworkStatus) {
		// Close may already have removed rules or stopped DNS before failing.
		s.Egress.State, s.Egress.Effective = protocol.EgressUnknown, nil
		s.Egress.Diagnostic = operationDiagnostic("network_release_failed", "release", err)
	})
	log.Printf("network: bed=%s release failed; retaining allocation: %v", b.ID.String(), err)
}

func operationDiagnostic(code, stage string, err error) *protocol.NetworkDiagnostic {
	return &protocol.NetworkDiagnostic{Code: code, Cause: string(hostel.CauseOf(err)), Stage: stage, Message: err.Error()}
}

var _ bed.Subsystem[Status] = (*Manager)(nil)
