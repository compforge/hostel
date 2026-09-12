// Package network owns Bed network selection, bindings and policy publication.
package network

import (
	"context"
	"sync"

	"github.com/qiankunli/hostel/internal/bed"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

// Status is the component status: mechanism facts plus the Bed execution scope.
type Status struct {
	Enabled bool              `json:"enabled"`
	Backend string            `json:"backend"`
	Scope   string            `json:"scope"`
	Reason  string            `json:"reason,omitempty"`
	Probe   hostnetwork.Probe `json:"probe"`
}

// These protocol types are shared with the network mechanism; Bed model
// conversion and enforcement scope remain owned here.
type Attachment = hostnetwork.Attachment
type Policy = hostnetwork.Policy
type Rule = hostnetwork.Rule
type PolicyMutation = hostnetwork.PolicyMutation
type PolicyStatus = hostnetwork.PolicyStatus

var ErrUnavailable = hostnetwork.ErrUnavailable
var ErrInvalidPolicy = hostnetwork.ErrInvalidPolicy

func NormalizePolicy(p Policy) (Policy, error) { return hostnetwork.NormalizePolicy(p) }

type Manager struct {
	bed.Noop
	provider    Provider
	pool        *hostnetwork.Pool
	status      bed.StatusWriter[bed.NetworkStatus]
	hookMu      sync.Mutex
	allocations map[*bed.Bed]*bedAllocation
}

func New(ctx context.Context) *Manager {
	pool := hostnetwork.New(ctx)
	return &Manager{pool: pool}
}
func (m *Manager) Start(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if starter, ok := m.provider.(interface{ Start(context.Context) error }); ok {
		return starter.Start(ctx)
	}
	if m.pool != nil {
		return m.pool.Start(ctx)
	}
	return nil
}
func (m *Manager) Status() Status {
	if m != nil && m.provider != nil {
		return m.provider.Status()
	}
	var pool *hostnetwork.Pool
	if m != nil {
		pool = m.pool
	}
	report := pool.Status()
	scope := "carrier"
	if report.Available {
		scope = "bed_processes"
	}
	return Status{Enabled: report.Available, Backend: report.Backend, Scope: scope, Reason: report.Reason, Probe: report.Probe}
}
func (m *Manager) Acquire(ctx context.Context, id string) (Attachment, error) {
	if m != nil && m.provider != nil {
		return m.provider.Acquire(ctx, id)
	}
	// Optional isolation is a Bed policy. The host pool itself returns an
	// unavailable error and never silently admits an unisolated allocation.
	if !m.Status().Enabled {
		return nil, nil
	}
	return m.pool.Acquire(ctx, id)
}
func (m *Manager) NetworkPolicy(ctx context.Context, id string, change PolicyMutation) (PolicyStatus, error) {
	if m != nil && m.provider != nil {
		return m.provider.NetworkPolicy(ctx, id, change)
	}
	if m == nil || m.pool == nil {
		return PolicyStatus{}, ErrUnavailable
	}
	s, err := m.pool.NetworkPolicy(ctx, id, change)
	if err == nil {
		s.Scope = "bed_processes"
	}
	return s, err
}
func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if m.provider != nil {
		return m.provider.Close(ctx)
	}
	return m.pool.Close(ctx)
}
