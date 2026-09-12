// Package network owns Bed network selection, bindings and policy publication.
package network

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/feature"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

// Status is the component status: mechanism facts plus the Bed execution scope.
type Status struct {
	Features map[string]feature.Status `json:"features"`
	Enabled  bool                      `json:"enabled"`
	Backend  string                    `json:"backend"`
	Scope    string                    `json:"scope"`
	Reason   string                    `json:"reason,omitempty"`
	Probe    hostnetwork.Probe         `json:"probe"`
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
	config Config
	bed.Noop
	provider    Provider
	pool        *hostnetwork.Pool
	status      bed.StatusWriter[bed.NetworkStatus]
	hookMu      sync.Mutex
	allocations map[*bed.Bed]*bedAllocation
}

func New(ctx context.Context) *Manager {
	return NewConfigured(ctx, Config{})
}
func NewConfigured(ctx context.Context, cfg Config) *Manager {
	m := &Manager{config: cfg}
	if cfg.NetNS.Effective() != feature.Off {
		m.pool = hostnetwork.New(ctx)
	}
	return m
}
func (m *Manager) Start(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if err := m.config.Validate(); err != nil {
		return fmt.Errorf("network: %w", err)
	}
	if m.config.NetNS.Effective() == feature.Off {
		log.Printf("network: feature=netns policy=off reason=disabled_by_config")
		return nil
	}
	if starter, ok := m.provider.(interface{ Start(context.Context) error }); ok {
		if err := starter.Start(ctx); err != nil {
			return err
		}
		return m.checkRequired()
	}
	if m.pool != nil {
		if err := m.pool.Start(ctx); err != nil {
			return err
		}
		return m.checkRequired()
	}
	return m.checkRequired()
}
func (m *Manager) checkRequired() error {
	return m.Status().Features["netns"].CheckRequired("network.netns")
}
func (m *Manager) Status() Status {
	if m != nil && m.provider != nil {
		return m.describe(m.provider.Status())
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
	return m.describe(Status{Enabled: report.Available, Backend: report.Backend, Scope: scope, Reason: report.Reason, Probe: report.Probe})
}
func (m *Manager) describe(s Status) Status {
	policy := feature.Auto
	if m != nil {
		policy = m.config.NetNS.Effective()
	}
	if policy == feature.Off {
		s.Enabled = false
		s.Backend = "shared"
		s.Scope = "carrier"
		s.Reason = "disabled_by_config"
	}
	requirements := feature.Requirements{Capabilities: []string{"CAP_NET_ADMIN", "CAP_SYS_ADMIN"}, Tools: []string{"ip", "nft", "setpriv"}, Conditions: []string{"Linux", "IPv4 forwarding", "DNS resolvers", "namespace entry and connectivity probe"}}
	s.Features = map[string]feature.Status{"netns": feature.Describe(policy, requirements, s.Probe.Stage != "", s.Enabled, s.Enabled, s.Reason)}
	return s
}
func (m *Manager) Acquire(ctx context.Context, id string) (Attachment, error) {
	if m != nil && m.config.NetNS.Effective() == feature.Off {
		return nil, nil
	}
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
	if m != nil && m.config.NetNS.Effective() == feature.Off {
		return PolicyStatus{}, ErrUnavailable
	}
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
