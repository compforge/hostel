// Package network owns Bed network selection, bindings and policy publication.
package network

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	hostel "github.com/qiankunli/hostel/internal"
	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/tool"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

// Status is the subsystem status: mechanism facts plus the Bed execution scope.
type Status struct {
	Expected  Level                  `json:"expected"`
	Effective Level                  `json:"effective"`
	Supported []Level                `json:"supported"`
	Tools     map[string]tool.Status `json:"tools"`
	Room      bed.RoomType           `json:"room"`
	Backend   string                 `json:"backend"`
	Scope     string                 `json:"scope"`
	Reason    string                 `json:"reason,omitempty"`
	Cause     hostel.ErrorCause      `json:"cause,omitempty"`
	Err       error                  `json:"-"`
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
	portMappings *PortMappings
	config       Config
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
	if err := m.Status().Tools["netns"].CheckRequired("network.netns"); err != nil {
		return fmt.Errorf("%s: %w", err, m.UnavailableError())
	}
	return nil
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
	level := Shared
	if report.Available {
		level = Private
	}
	return m.describe(Status{Effective: level, Backend: report.Backend, Scope: scope, Reason: report.Reason, Cause: report.Cause, Err: report.Probe.Err, Probe: report.Probe})
}

func (m *Manager) describe(s Status) Status {
	available := s.Effective == Private
	policy := tool.Auto
	if m != nil {
		policy = m.config.NetNS.Effective()
	}
	if policy == tool.Off {
		s.Effective = Shared
		s.Backend = "shared"
		s.Scope = "carrier"
		s.Reason = "disabled_by_config"
		s.Err = hostel.WrapError(hostel.ErrUnavailable, "network namespace disabled", hostel.CauseRequest, ErrUnavailable)
	}
	s.Expected = Private
	selected := s.Effective == Private
	s.Effective, s.Supported = Shared, []Level{Shared}
	if available && policy != tool.Off {
		s.Supported = append(s.Supported, Private)
	}
	if selected {
		s.Effective = Private
		s.Err, s.Cause = nil, ""
	}
	if !selected {
		if s.Err == nil {
			// No failure evidence was supplied; do not infer attribution from a status label.
			s.Err = hostel.WrapError(hostel.ErrUnavailable, "network namespace", hostel.CauseUnknown, ErrUnavailable)
		}
		s.Cause = hostel.CauseOf(s.Err)
	}
	requirements := tool.Requirements{Capabilities: []string{"CAP_NET_ADMIN", "CAP_SYS_ADMIN"}, Tools: []string{"ip", "nft"}, Conditions: []string{"Linux", "IPv4 forwarding", "DNS resolvers", "namespace entry and connectivity probe"}}
	s.Tools = map[string]tool.Status{"netns": tool.Describe(policy, requirements, s.Probe.Stage != "", available, selected, s.Reason)}
	s.Room = s.Effective.Room()
	return s
}
func (m *Manager) Acquire(ctx context.Context, id string) (Attachment, error) {
	if m.Status().Effective != Private {
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
	if m.Status().Effective != Private {
		return PolicyStatus{}, m.UnavailableError()
	}
	if m != nil && m.provider != nil {
		return m.provider.NetworkPolicy(ctx, id, change)
	}
	if m == nil || m.pool == nil {
		return PolicyStatus{}, m.UnavailableError()
	}
	s, err := m.pool.NetworkPolicy(ctx, id, change)
	if err == nil {
		s.Scope = "bed_processes"
	}
	return s, err
}

// UnavailableError carries the same attribution exposed in the capability report.
// Raw probe errors stay in operator status/logs, not client-facing messages.
func (m *Manager) UnavailableError() error {
	err := m.Status().Err
	return hostel.WrapError(hostel.ErrUnavailable, "bed network policy", hostel.CauseOf(err), errors.Join(ErrUnavailable, err))
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
