// Package network owns Bed network selection, bindings and policy publication.
package network

import (
	"context"
	"fmt"
	"log"
	"sync"

	hostel "github.com/qiankunli/hostel/internal"
	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/tool"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
	"github.com/qiankunli/hostel/protocol"
)

type IsolationStatus struct {
	protocol.NetworkIsolationStatus
	Supported []Level           `json:"supported"`
	Probe     hostnetwork.Probe `json:"probe"`
}
type EgressCapability struct {
	Backend    string                      `json:"backend"`
	Diagnostic *protocol.NetworkDiagnostic `json:"diagnostic,omitempty"`
	Probe      hostnetwork.Probe           `json:"probe"`
}

// Status reports independent isolation and egress mechanism choices for new Beds.
type Status struct {
	Isolation IsolationStatus        `json:"isolation"`
	Egress    EgressCapability       `json:"egress"`
	Tools     map[string]tool.Status `json:"tools"`
}

// These protocol types are shared with the network mechanism; Bed model
// conversion and enforcement scope remain owned here.
type Attachment = hostnetwork.Attachment
type Policy = hostnetwork.Policy
type Rule = hostnetwork.Rule
type PolicyMutation = hostnetwork.PolicyMutation

var ErrUnavailable = hostnetwork.ErrUnavailable
var ErrInvalidPolicy = hostnetwork.ErrInvalidPolicy

func NormalizePolicy(p Policy) (Policy, error) { return hostnetwork.NormalizePolicy(p) }

type Manager struct {
	stateDirectory   string
	allocationsStore *hostnetwork.AllocationStore
	portMappings     *PortMappings
	config           Config
	ports            *hostnetwork.PortManager
	dedicatedUIDs    bool
	uidPolicies      hostnetwork.UIDPolicyProvider
	uidProbe         hostnetwork.Probe
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
	m.ports = ports
	if m.pool != nil {
		m.pool.SetPortManager(ports)
	}
}

// SetDedicatedUIDs binds Privilege's actual startup choice, not its supported capabilities.
func (m *Manager) SetDedicatedUIDs(enabled bool) { m.dedicatedUIDs = enabled }

func NewConfigured(ctx context.Context, cfg Config) *Manager {
	m := &Manager{config: cfg}
	if cfg.NetNS.Effective() != tool.Off {
		m.pool = hostnetwork.New(ctx)
	}
	return m
}
func (m *Manager) SetStateDirectory(path string) { m.stateDirectory = path }

func (m *Manager) Start(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if err := m.config.Validate(); err != nil {
		return fmt.Errorf("network: %w", err)
	}
	if m.stateDirectory != "" && m.allocationsStore == nil {
		var err error
		m.allocationsStore, err = hostnetwork.OpenAllocationStore(ctx, m.stateDirectory)
		if err != nil {
			return fmt.Errorf("network recovery: %w", err)
		}
		if m.pool != nil {
			m.pool.SetAllocationStore(m.allocationsStore)
		}
	}
	if m.config.NetNS.Effective() == tool.Off {
		log.Printf("network: tool=netns policy=off reason=disabled_by_config")
	} else if starter, ok := m.provider.(interface{ Start(context.Context) error }); ok {
		if err := starter.Start(ctx); err != nil {
			return err
		}
	} else if m.pool != nil {
		if err := m.pool.Start(ctx); err != nil {
			return err
		}
	}
	if err := m.checkRequired(); err != nil {
		return err
	}
	if m.Status().Isolation.Level == string(Shared) && m.dedicatedUIDs && m.config.UIDNFT.Effective() != tool.Off && m.uidPolicies == nil {
		m.uidPolicies, m.uidProbe = hostnetwork.ProbeUIDPolicy(ctx, m.ports, m.allocationsStore)
		log.Printf("network: tool=uid_nft available=%t stage=%s cause=%s reason=%q", m.uidPolicies != nil, m.uidProbe.Stage, m.uidProbe.Cause, m.uidProbe.Error)
	}
	return m.Status().Tools["uid_nft"].CheckRequired("network.uid_nft")
}
func (m *Manager) checkRequired() error {
	if err := m.Status().Tools["netns"].CheckRequired("network.netns"); err != nil {
		return fmt.Errorf("%s: %w", err, m.Status().Isolation.Probe.Err)
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
	return m.describe(pool.Status())
}

func (m *Manager) describe(report hostnetwork.Status) Status {
	netnsPolicy, uidPolicy := tool.Auto, tool.Auto
	uidAvailable, dedicated := false, false
	var uidProbe hostnetwork.Probe
	if m != nil {
		netnsPolicy, uidPolicy = m.config.NetNS.Effective(), m.config.UIDNFT.Effective()
		uidAvailable, dedicated, uidProbe = m.uidPolicies != nil, m.dedicatedUIDs, m.uidProbe
	}
	selected := report.Available && netnsPolicy != tool.Off
	s := Status{Isolation: IsolationStatus{NetworkIsolationStatus: protocol.NetworkIsolationStatus{Level: string(Shared), Room: bed.Dorm}, Supported: []Level{Shared}, Probe: report.Probe}, Egress: EgressCapability{Backend: "none", Probe: uidProbe}}
	if selected {
		s.Isolation.Level, s.Isolation.Room = string(Private), bed.Suite
		s.Isolation.Supported = append(s.Isolation.Supported, Private)
		s.Isolation.Probe.Err = nil
		s.Egress.Backend, s.Egress.Probe = "netns_nft", report.Probe
	} else {
		code, reason, probe := "netns_unavailable", report.Reason, report.Probe
		if netnsPolicy == tool.Off {
			code, reason = "disabled_by_config", "Network namespace is disabled by configuration"
			probe = hostnetwork.Probe{Err: hostel.WrapError(hostel.ErrUnavailable, "network namespace disabled", hostel.CauseRequest, ErrUnavailable)}
		} else if probe.Stage == "forwarding" {
			code = "ipv4_forwarding_unavailable"
		}
		if probe.Err == nil {
			probe.Err = hostel.WrapError(hostel.ErrUnavailable, "network namespace", hostel.CauseUnknown, ErrUnavailable)
		}
		s.Isolation.Probe = probe
		s.Isolation.Diagnostic = &protocol.NetworkDiagnostic{Code: code, Cause: string(hostel.CauseOf(probe.Err)), Stage: probe.Stage, Message: reason}
	}
	netnsReason := ""
	if d := s.Isolation.Diagnostic; d != nil {
		netnsReason = d.Message
	}
	s.Tools = map[string]tool.Status{"netns": tool.Describe(netnsPolicy, tool.Requirements{Capabilities: []string{"CAP_NET_ADMIN", "CAP_SYS_ADMIN"}, Tools: []string{"ip", "nft"}, Conditions: []string{"Linux", "IPv4 forwarding", "DNS resolvers", "namespace entry and connectivity probe"}}, s.Isolation.Probe.Stage != "", report.Available, selected, netnsReason)}
	uidSelected := !selected && uidAvailable
	uidReason := "dedicated identity not selected"
	if dedicated {
		uidReason = uidProbe.Error
	}
	s.Tools["uid_nft"] = tool.Describe(uidPolicy, tool.Requirements{Capabilities: []string{"CAP_NET_ADMIN"}, Tools: []string{"nft"}, Conditions: []string{"Linux", "dedicated workload UID", "DNS resolvers", "socket UID filter and DNS NAT"}}, uidProbe.Stage != "", uidAvailable, uidSelected, uidReason)
	if uidSelected {
		s.Egress.Backend = "uid_nft"
	}
	if s.Egress.Backend == "none" {
		d := &protocol.NetworkDiagnostic{Code: "uid_nft_not_probed", Cause: string(hostel.CauseUnknown), Stage: "probe", Message: "UID egress has not been probed"}
		switch {
		case uidPolicy == tool.Off:
			d.Code, d.Cause, d.Stage, d.Message = "disabled_by_config", string(hostel.CauseRequest), "configuration", "UID egress is disabled by configuration"
		case !dedicated:
			d.Code, d.Stage, d.Message = "dedicated_identity_unavailable", "identity", "Dedicated workload identity is not selected"
		case uidProbe.Stage != "":
			d.Code, d.Cause, d.Stage, d.Message = "uid_nft_unavailable", string(uidProbe.Cause), uidProbe.Stage, uidProbe.Error
		}
		s.Egress.Diagnostic = d
	}
	return s
}
func (m *Manager) Acquire(ctx context.Context, id string) (Attachment, error) {
	if m.Status().Isolation.Level != string(Private) {
		return nil, nil
	}
	if m != nil && m.provider != nil {
		return m.provider.Acquire(ctx, id)
	}
	// Optional isolation is a Bed policy. The host pool itself returns an
	// unavailable error and never silently admits an unisolated allocation.
	return m.pool.Acquire(ctx, id)
}
func (m *Manager) NetworkPolicy(ctx context.Context, id string, change PolicyMutation) (Policy, error) {
	if m.Status().Isolation.Level != string(Private) {
		return Policy{}, m.UnavailableError()
	}
	if m != nil && m.provider != nil {
		return m.provider.NetworkPolicy(ctx, id, change)
	}
	if m == nil || m.pool == nil {
		return Policy{}, m.UnavailableError()
	}
	return m.pool.NetworkPolicy(ctx, id, change)
}

// UnavailableError carries the same attribution exposed in the capability report.
// Raw probe errors stay in operator status/logs, not client-facing messages.
func (m *Manager) UnavailableError() error {
	cause := hostel.CauseUnknown
	if d := m.Status().Egress.Diagnostic; d != nil {
		cause = hostel.ErrorCause(d.Cause)
	}
	return hostel.WrapError(hostel.ErrUnavailable, "bed network policy", cause, ErrUnavailable)
}

func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if m.uidPolicies != nil {
		if err := m.uidPolicies.Close(ctx); err != nil {
			return err
		}
	}
	var err error
	if m.provider != nil {
		err = m.provider.Close(ctx)
	} else {
		err = m.pool.Close(ctx)
	}
	if err != nil {
		return err
	}
	return m.allocationsStore.Close()
}
