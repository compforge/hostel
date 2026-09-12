package network

import (
	"context"
	"github.com/qiankunli/hostel/internal/bed"
)

type Provider interface {
	Acquire(context.Context, string) (Attachment, error)
	NetworkPolicy(context.Context, string, PolicyMutation) (PolicyStatus, error)
	Diagnostics() Report
	Close(context.Context) error
}

// WithProvider supplies the namespace implementation; useful for capability
// fallbacks and deterministic lifecycle tests without host mutation.
func WithProvider(p Provider, status bed.StatusWriter[bed.NetworkStatus]) *Manager {
	return &Manager{provider: p, status: status}
}
func (m *Manager) SetStatusWriter(status bed.StatusWriter[bed.NetworkStatus]) { m.status = status }
func (m *Manager) Prepare(ctx context.Context, b *bed.Bed) error {
	attachment, err := m.Acquire(ctx, b.ID)
	if err != nil {
		return err
	}
	m.hookMu.Lock()
	if m.allocations == nil {
		m.allocations = make(map[*bed.Bed]Attachment)
	}
	m.allocations[b] = attachment
	m.hookMu.Unlock()
	status := bed.NetworkStatus{Enabled: m.Diagnostics().Enabled}
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
	m.hookMu.Lock()
	defer m.hookMu.Unlock()
	return m.allocations[b]
}
func (m *Manager) Release(ctx context.Context, b *bed.Bed) error {
	if a := m.Attachment(b); a != nil {
		if err := a.Close(ctx); err != nil {
			return err
		}
	}
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
	if m.Attachment(b) == nil {
		return PolicyStatus{}, ErrUnavailable
	}
	result, err := m.NetworkPolicy(ctx, b.ID, mutation)
	if err == nil {
		m.status.Update(b, func(s *bed.NetworkStatus) { s.Policy = ToModel(result.Policy) })
	}
	return result, err
}

var _ bed.Component[Report] = (*Manager)(nil)
