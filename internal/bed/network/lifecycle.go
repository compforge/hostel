package network

import (
	"context"
	"fmt"
	"log"
	"slices"

	"github.com/qiankunli/hostel/internal/bed"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
	"golang.org/x/sync/semaphore"
)

// Policy application and publication share this allocation lock, so concurrent
// updates cannot publish an older policy after a newer one has taken effect.
type bedAllocation struct {
	mu         *semaphore.Weighted
	attachment Attachment
	policy     hostnetwork.PolicyLease
	released   bool
	requested  *Policy
}

type Provider interface {
	Acquire(context.Context, string) (Attachment, error)
	NetworkPolicy(context.Context, string, PolicyMutation) (PolicyStatus, error)
	Status() Status
	Close(context.Context) error
}

// WithProvider supplies the namespace implementation; useful for capability
// fallbacks and deterministic lifecycle tests without host mutation.
func WithProvider(p Provider, status bed.StatusWriter[bed.NetworkStatus]) *Manager {
	return &Manager{provider: p, status: status}
}
func (m *Manager) SetStatusWriter(status bed.StatusWriter[bed.NetworkStatus]) { m.status = status }
func (m *Manager) Prepare(ctx context.Context, b *bed.Bed) error {
	attachment, err := m.Acquire(ctx, b.ID.String())
	if err != nil {
		return err
	}
	m.hookMu.Lock()
	if m.allocations == nil {
		m.allocations = make(map[*bed.Bed]*bedAllocation)
	}
	a := &bedAllocation{mu: semaphore.NewWeighted(1), attachment: attachment}
	m.allocations[b] = a
	m.hookMu.Unlock()
	if attachment == nil && m.uidPolicies != nil {
		identity := b.Status().Privilege
		if !m.dedicatedUIDs || !identity.Assigned {
			return fmt.Errorf("UID network policy requires a prepared dedicated identity")
		}
		a.policy, err = m.uidPolicies.Acquire(ctx, identity.UID)
		if err != nil {
			return fmt.Errorf("prepare UID network policy: %w", err)
		}
	}
	report := m.Status()
	level := Shared
	if attachment != nil {
		level = Private
	}
	if report.Effective == Private && attachment == nil {
		return fmt.Errorf("private network allocation returned no attachment")
	}
	status := bed.NetworkStatus{PolicyBackend: report.PolicyBackend, Level: string(level), PolicyStatus: "none", Reason: report.Reason, Cause: string(report.Cause)}
	if attachment != nil {
		status.Gateway = attachment.Gateway()
	}
	m.status.Set(b, status)
	if policy := b.Spec().NetworkPolicy; policy != nil {
		p := FromModel(*policy)
		_, err = m.UpdatePolicy(ctx, b, PolicyMutation{Replace: &p})
	}
	if err != nil {
		return err
	}
	m.status.Update(b, func(s *bed.NetworkStatus) { s.Room = level.Room() })
	return nil
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
			return err
		}
	}
	if a.attachment != nil {
		if err := a.attachment.Close(ctx); err != nil {
			return err
		}
	}
	a.released = true
	m.hookMu.Lock()
	delete(m.allocations, b)
	m.hookMu.Unlock()
	m.status.Set(b, bed.NetworkStatus{})
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
func (m *Manager) UpdatePolicy(ctx context.Context, b *bed.Bed, mutation PolicyMutation) (PolicyStatus, error) {
	a := m.allocation(b)
	if a == nil {
		return PolicyStatus{}, m.UnavailableError()
	}
	if err := a.mu.Acquire(ctx, 1); err != nil {
		return PolicyStatus{}, err
	}
	defer a.mu.Release(1)
	if a.released {
		return PolicyStatus{}, m.UnavailableError()
	}
	if a.attachment == nil && a.policy == nil {
		current := Policy{DefaultAction: "allow"}
		if a.requested != nil {
			current = *a.requested
		}
		desired, err := hostnetwork.ApplyMutation(current, mutation)
		if err != nil {
			return PolicyStatus{}, err
		}
		changed := mutation.Replace != nil || mutation.Merge != nil || mutation.Remove != nil
		if changed {
			a.requested = &desired
		}
		report := m.Status()
		result := PolicyStatus{Status: "degraded", Mode: "unenforced", EnforcementMode: "none", Scope: "bed_processes", Policy: Policy{DefaultAction: "allow"}, Reason: report.Reason, Cause: report.Cause}
		if a.requested != nil {
			copy := desired
			copy.Egress = slices.Clone(desired.Egress)
			result.RequestedPolicy = &copy
			m.status.Update(b, func(s *bed.NetworkStatus) {
				s.RequestedPolicy = ToModel(desired)
				s.Policy = nil
				s.PolicyStatus = "unenforced"
			})
		}
		if changed {
			log.Printf("network: bed=%s policy=unenforced cause=%s reason=%q", b.ID.String(), report.Cause, report.Reason)
		}
		return result, nil
	}
	var result PolicyStatus
	var err error
	if a.policy != nil {
		result, err = a.policy.Update(ctx, mutation)
		result.Scope = "bed_processes"
	} else {
		result, err = m.NetworkPolicy(ctx, b.ID.String(), mutation)
	}
	if err == nil {
		if mutation.Replace != nil || mutation.Merge != nil || mutation.Remove != nil {
			log.Printf("network: bed=%s policy=enforced backend=%s rules=%d", b.ID.String(), m.Status().PolicyBackend, len(result.Policy.Egress))
		}
		m.status.Update(b, func(s *bed.NetworkStatus) {
			s.Policy = ToModel(result.Policy)
			s.RequestedPolicy = ToModel(result.Policy)
			s.PolicyStatus = "enforced"
		})
	}
	return result, err
}

var _ bed.Subsystem[Status] = (*Manager)(nil)
