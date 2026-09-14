package network

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"syscall"
)

var ErrPortsExhausted = errors.New("port pool exhausted")

// PortManager owns daemon-wide TCP allocations, not application lifecycles.
// An external candidate is only a logical reservation: the consumer must
// validate readiness and disclose whether listener ownership was verifiable.
type PortManager struct {
	mu                sync.Mutex
	first, last, next int
	closed            bool
	allocations       map[*PortAllocation]struct{}
}

type PortStatus struct {
	Owner   string `json:"owner"`
	Scope   string `json:"scope"`
	Address string `json:"address"`
	State   string `json:"state"`
}

type PortAllocation struct {
	manager  *PortManager
	status   PortStatus
	host     string
	port     int
	listener net.Listener
}

func NewPortManager(first, last int) (*PortManager, error) {
	if first < 1 || last > 65535 || first > last {
		return nil, fmt.Errorf("invalid port range %d-%d", first, last)
	}
	return &PortManager{first: first, last: last, next: first, allocations: make(map[*PortAllocation]struct{})}, nil
}

// Reserve accepts literal IP addresses; an empty scope means the carrier
// network namespace. Port zero chooses from the configured service pool.
func (m *PortManager) Reserve(owner, scope, address string) (*PortAllocation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	attempts := 1
	if _, port, err := net.SplitHostPort(address); err == nil && port == "0" {
		attempts = m.last - m.first + 1
	}
	for range attempts {
		a, err := m.reserve(owner, scope, address)
		if err != nil {
			return nil, err
		}
		if scope != "" {
			return a, nil // The daemon cannot bind in another network scope.
		}
		// Exclude existing external listeners before starting a process. Closing
		// this probe does not eliminate the subsequent application bind race.
		ln, err := net.Listen("tcp", a.status.Address)
		if err == nil {
			err = ln.Close()
			if err == nil {
				return a, nil
			}
		}
		delete(m.allocations, a)
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, fmt.Errorf("check candidate listener: %w", err)
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrPortsExhausted, address)
}

func (m *PortManager) reserve(owner, scope, address string) (*PortAllocation, error) {
	if m.closed {
		return nil, net.ErrClosed
	}
	host, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if host != "" {
		if _, err := netip.ParseAddr(host); err != nil {
			return nil, fmt.Errorf("port allocation requires a literal IP: %w", err)
		}
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 0 || port > 65535 || owner == "" {
		return nil, fmt.Errorf("invalid port request")
	}
	count := 1
	if port == 0 {
		count = m.last - m.first + 1
	}
	for range count {
		candidate := port
		if candidate == 0 {
			candidate = m.next
			m.next++
			if m.next > m.last {
				m.next = m.first
			}
		}
		conflict := false
		for a := range m.allocations {
			if a.status.Scope == scope && a.port == candidate && overlappingHosts(a.host, host) {
				conflict = true
				break
			}
		}
		if conflict {
			continue
		}
		a := &PortAllocation{manager: m, host: host, port: candidate, status: PortStatus{Owner: owner, Scope: scope, Address: net.JoinHostPort(host, strconv.Itoa(candidate)), State: "reserved"}}
		m.allocations[a] = struct{}{}
		return a, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrPortsExhausted, address)
}

func overlappingHosts(a, b string) bool {
	// Treat wildcard IPv6 as dual-stack. Being conservative also prevents
	// platform-specific IPV6_V6ONLY defaults from creating allocator holes.
	if a == "" || b == "" || a == "::" || b == "::" {
		return true
	}
	x, _ := netip.ParseAddr(a)
	y, _ := netip.ParseAddr(b)
	x = x.Unmap()
	y = y.Unmap()
	return x == y || (x.Is4() == y.Is4() && (x.IsUnspecified() || y.IsUnspecified()))
}

// Listen owns the kernel reservation continuously; Close via the allocation.
func (m *PortManager) Listen(owner, address string) (*PortAllocation, net.Listener, error) {
	resolved, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		return nil, nil, err
	}
	address = resolved.String()
	m.mu.Lock()
	defer m.mu.Unlock()
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, nil, err
	}
	attempts := 1
	if port == "0" {
		attempts = m.last - m.first + 1
	}
	for range attempts {
		a, err := m.reserve(owner, "", address)
		if err != nil {
			return nil, nil, err
		}
		ln, err := net.Listen("tcp", a.status.Address)
		if err != nil {
			delete(m.allocations, a)
			if port != "0" {
				return nil, nil, err
			}
			continue
		}
		a.listener = ln
		a.status.State = "listening"
		return a, ln, nil
	}
	return nil, nil, ErrPortsExhausted
}

func (a *PortAllocation) Address() string { return a.status.Address }
func (a *PortAllocation) Port() int       { return a.port }
func (a *PortAllocation) Confirm() error {
	m := a.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.allocations[a]; !ok {
		return net.ErrClosed
	}
	a.status.State = "listening"
	return nil
}

// Unconfirm retains desired ownership across an amenity's idle stop without
// reporting a nonexistent listener as currently bound.
func (a *PortAllocation) Unconfirm() {
	m := a.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.allocations[a]; ok && a.listener == nil {
		a.status.State = "reserved"
	}
}
func (a *PortAllocation) Release() error {
	m := a.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.allocations[a]; !ok {
		return nil
	}
	if a.listener != nil {
		if err := a.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			return err
		}
	}
	delete(m.allocations, a)
	return nil
}
func (m *PortManager) Status() []PortStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PortStatus, 0, len(m.allocations))
	for a := range m.allocations {
		out = append(out, a.status)
	}
	return out
}
func (m *PortManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	var result error
	for a := range m.allocations {
		if a.listener != nil {
			result = errors.Join(result, a.listener.Close())
		}
		delete(m.allocations, a)
	}
	return result
}
