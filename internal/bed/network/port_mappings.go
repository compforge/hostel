package network

import (
	"errors"
	"fmt"
	"log"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/qiankunli/hostel/internal/bed"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

// PortMappings owns runtime port resources for Bed consumers. Service readiness
// stays with Service; this registry neither probes nor starts application code.
type PortMappings struct {
	mu        sync.Mutex
	ports     *hostnetwork.PortManager
	advertise string
	beds      map[*bed.Bed]map[string]*PortMapping
}

type PortMappingStatus struct {
	Name            string `json:"name"`
	Protocol        string `json:"protocol"`
	Service         string `json:"service"`
	ExecutionID     string `json:"execution_id,omitempty"`
	Network         string `json:"network"`
	State           string `json:"state"`
	BedPort         int    `json:"bed_port"`
	HostPort        int    `json:"host_port,omitempty"`
	InternalAddress string `json:"internal_address"`
	ExternalAddress string `json:"external_address,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

// PortMapping is an exact allocation handle; release never addresses resources
// by reusable Bed name, port number or Service name alone.
type PortMapping struct {
	mu           sync.Mutex
	owner        *PortMappings
	bed          *bed.Bed
	spec         bed.PortMappingSpec
	allocation   *hostnetwork.PortAllocation
	forward      *hostnetwork.TCPForwarder
	probeAddress string
	status       PortMappingStatus
}

func NewPortMappings(ports *hostnetwork.PortManager, advertise string) *PortMappings {
	return &PortMappings{ports: ports, advertise: advertise, beds: make(map[*bed.Bed]map[string]*PortMapping)}
}
func (m *PortMappings) Available(publish bool) bool {
	return m != nil && m.ports != nil && (!publish || m.advertise != "")
}

func (m *PortMappings) Reserve(b *bed.Bed, name, service, scope, host string, avoidPreferred bool) (*PortMapping, error) {
	var spec bed.PortMappingSpec
	for _, s := range b.Spec().PortMappings {
		if s.Name == name {
			spec = s
			break
		}
	}
	if spec.Name == "" {
		return nil, fmt.Errorf("undeclared port mapping %q", name)
	}
	if !m.Available(spec.Publish) {
		return nil, fmt.Errorf("port mapping %s: port manager or advertised host unavailable", name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.beds[b][name] != nil {
		return nil, fmt.Errorf("port mapping %s is already allocated", name)
	}
	port, reason := 0, ""
	if spec.RequireBedPort || (scope != "" && !avoidPreferred) {
		port = spec.BedPort
	}
	if port == 0 && spec.BedPort != 0 {
		reason = "SharedNetworkUsesHostPort"
		if scope != "" {
			reason = "PreferredPortOccupied"
		}
	}
	owner := b.ID.String() + "/port/" + name
	listenHost := "0.0.0.0"
	if scope == "" && !spec.Publish {
		listenHost = "127.0.0.1"
	}
	allocation, err := m.ports.Reserve(owner, scope, net.JoinHostPort(listenHost, strconv.Itoa(port)))
	if err != nil && port != 0 && !spec.RequireBedPort && errors.Is(err, hostnetwork.ErrPortsExhausted) {
		reason = "PreferredPortOccupied"
		allocation, err = m.ports.Reserve(owner, scope, net.JoinHostPort(listenHost, "0"))
	}
	if err != nil {
		return nil, fmt.Errorf("port mapping %s: %w", name, err)
	}
	mode := "shared"
	if scope != "" {
		mode = "private"
	}
	p := &PortMapping{owner: m, bed: b, spec: spec, allocation: allocation, probeAddress: net.JoinHostPort(host, strconv.Itoa(allocation.Port())), status: PortMappingStatus{
		Name: name, Protocol: spec.Protocol, Service: service, Network: mode, State: "reserved", BedPort: allocation.Port(), InternalAddress: net.JoinHostPort("127.0.0.1", strconv.Itoa(allocation.Port())), Reason: reason,
	}}
	if m.beds[b] == nil {
		m.beds[b] = make(map[string]*PortMapping)
	}
	m.beds[b][name] = p
	log.Printf("hostel port mapping reserved: bed=%s mapping=%s network=%s bed_port=%d reason=%s", b.ID, name, mode, p.status.BedPort, reason)
	return p, nil
}
func (p *PortMapping) ListenAddress() string { return p.allocation.Address() }
func (p *PortMapping) ProbeAddress() string  { return p.probeAddress }
func (p *PortMapping) Port() int             { return p.allocation.Port() }
func (p *PortMapping) Bind(execution string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status.ExecutionID = execution
}
func (p *PortMapping) Status() PortMappingStatus { p.mu.Lock(); defer p.mu.Unlock(); return p.status }

// Publish follows readiness. Private networks publish a separate carrier
// listener; shared networks use the application's same port without a proxy.
func (p *PortMapping) Publish() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.status.State != "reserved" {
		return fmt.Errorf("port mapping %s is not reserved", p.spec.Name)
	}
	if err := p.allocation.Confirm(); err != nil {
		return err
	}
	if p.spec.Publish {
		port := p.allocation.Port()
		if p.status.Network == "private" {
			forward, err := hostnetwork.NewTCPForwarder(p.owner.ports, p.bed.ID.String()+"/port/"+p.spec.Name+"/publish", p.probeAddress)
			if err != nil {
				return err
			}
			p.forward = forward
			port = forward.Port()
		}
		p.status.HostPort = port
		p.status.ExternalAddress = net.JoinHostPort(p.owner.advertise, strconv.Itoa(port))
	}
	p.status.State = "listening"
	return nil
}

// Withdraw closes forwarding connections before the consumer is stopped. The
// internal reservation remains owned until process cleanup succeeds.
func (p *PortMapping) Withdraw() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.status.State == "released" {
		return nil
	}
	p.status.State = "stopping"
	p.status.ExternalAddress = ""
	p.status.HostPort = 0
	if p.forward != nil {
		if err := p.forward.Close(); err != nil {
			return err
		}
		p.forward = nil
	}
	return nil
}
func (p *PortMapping) Release() error {
	if err := p.Withdraw(); err != nil {
		return err
	}
	p.mu.Lock()
	if err := p.allocation.Release(); err != nil {
		p.mu.Unlock()
		return err
	}
	p.status.State = "released"
	p.mu.Unlock()
	m := p.owner
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.beds[p.bed][p.spec.Name] == p {
		delete(m.beds[p.bed], p.spec.Name)
		if len(m.beds[p.bed]) == 0 {
			delete(m.beds, p.bed)
		}
	}
	return nil
}
func (m *PortMappings) Status(b *bed.Bed) []PortMappingStatus {
	out := []PortMappingStatus{}
	if m == nil {
		return out
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.beds[b] {
		out = append(out, p.Status())
	}
	slices.SortFunc(out, func(a, b PortMappingStatus) int { return strings.Compare(a.Name, b.Name) })
	return out
}
func (m *PortMappings) CheckReleased(b *bed.Bed) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.beds[b]) != 0 {
		return fmt.Errorf("bed %s still owns port mappings", b.ID)
	}
	return nil
}
