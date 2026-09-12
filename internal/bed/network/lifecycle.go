package network

import (
	"context"
	"github.com/qiankunli/hostel/internal/bed"
	"golang.org/x/sync/semaphore"
)

// Policy application and publication share this allocation lock, so concurrent
// updates cannot publish an older policy after a newer one has taken effect.
type bedAllocation struct {
	mu         *semaphore.Weighted
	attachment Attachment
	released   bool
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
	m.allocations[b] = &bedAllocation{mu: semaphore.NewWeighted(1), attachment: attachment}
	m.hookMu.Unlock()
	status := bed.NetworkStatus{Enabled: m.Status().Enabled}
	if attachment != nil {
		status.Gateway = attachment.Gateway()
	}
	m.status.Set(b, status)
	if policy := b.Spec().NetworkPolicy; policy != nil {
		p := FromModel(*policy)
		_, err = m.UpdatePolicy(ctx, b, PolicyMutation{Replace: &p})
	}
	return err
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
func (m *Manager) Release(ctx context.Context, b *bed.Bed) error {
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
		return PolicyStatus{}, ErrUnavailable
	}
	if err := a.mu.Acquire(ctx, 1); err != nil {
		return PolicyStatus{}, err
	}
	defer a.mu.Release(1)
	if a.released || a.attachment == nil {
		return PolicyStatus{}, ErrUnavailable
	}
	result, err := m.NetworkPolicy(ctx, b.ID.String(), mutation)
	if err == nil {
		m.status.Update(b, func(s *bed.NetworkStatus) { s.Policy = ToModel(result.Policy) })
	}
	return result, err
}

var _ bed.Component[Status] = (*Manager)(nil)
