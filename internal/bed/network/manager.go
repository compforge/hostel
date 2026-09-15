// Package network owns Bed network selection, bindings and policy publication.
package network

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/tool"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

// Status is the component status: mechanism facts plus the Bed execution scope.
type Status struct {
	Expected  Level                  `json:"expected"`
	Effective Level                  `json:"effective"`
	Supported []Level                `json:"supported"`
	Tools     map[string]tool.Status `json:"tools"`
	Enabled   bool                   `json:"enabled"`
	Backend   string                 `json:"backend"`
	Scope     string                 `json:"scope"`
	Reason    string                 `json:"reason,omitempty"`
	Probe     hostnetwork.Probe      `json:"probe"`
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

// SetPortManager supplies the daemon's allocator before network startup.
func (m *Manager) SetPortManager(ports *hostnetwork.PortManager) {
	if m.pool != nil {
		m.pool.SetPortManager(ports)
	}
}
func NewConfigured(ctx context.Context, cfg Config) *Manager {
	m := &Manager{config: cfg}
	if cfg.NetNS.Effective() != tool.Off {
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
	if m.config.NetNS.Effective() == tool.Off {
		log.Printf("network: tool=netns policy=off reason=disabled_by_config")
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
	return m.Status().Tools["netns"].CheckRequired("network.netns")
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
func (m *Manager) LevelStatus() bed.LevelStatus {
	status := m.Status()
	levels := make([]bed.Level, len(status.Supported))
	for i, level := range status.Supported {
		levels[i] = level
	}
	return bed.LevelStatus{Supported: levels}
}

func (m *Manager) describe(s Status) Status {
	available := s.Enabled
	policy := tool.Auto
	if m != nil {
		policy = m.config.NetNS.Effective()
	}
	if policy == tool.Off {
		s.Enabled = false
		s.Backend = "shared"
		s.Scope = "carrier"
		s.Reason = "disabled_by_config"
	}
	s.Expected = Private
	if m != nil && m.config.Level == Shared {
		s.Expected = Shared
		s.Enabled, s.Backend, s.Scope = false, "shared", "carrier"
		if policy != tool.Off {
			s.Reason = "shared network selected by profile"
		}
	}
	if m != nil && m.config.FallbackReason != "" {
		s.Expected = Private
		s.Reason = m.config.FallbackReason
	}
	s.Effective, s.Supported = Shared, []Level{Shared}
	if available && policy != tool.Off {
		s.Supported = append(s.Supported, Private)
	}
	if s.Enabled {
		s.Effective = Private
	}
	requirements := tool.Requirements{Capabilities: []string{"CAP_NET_ADMIN", "CAP_SYS_ADMIN"}, Tools: []string{"ip", "nft"}, Conditions: []string{"Linux", "IPv4 forwarding", "DNS resolvers", "namespace entry and connectivity probe"}}
	s.Tools = map[string]tool.Status{"netns": tool.Describe(policy, requirements, s.Probe.Stage != "", available, s.Enabled, s.Reason)}
	return s
}
func (m *Manager) Acquire(ctx context.Context, id string) (Attachment, error) {
	if !m.Status().Enabled {
		return nil, nil
	}
	if m != nil && m.provider != nil {
		return m.provider.Acquire(ctx, id)
	}
	// Optional isolation is a Bed policy. The host pool itself returns an
	// unavailable error and never silently admits an unisolated allocation.
	return m.pool.Acquire(ctx, id)
}
func (m *Manager) NetworkPolicy(ctx context.Context, id string, change PolicyMutation) (PolicyStatus, error) {
	if !m.Status().Enabled {
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
