// Package network provides isolated process networks, DNS and egress enforcement.
package network

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"sync"
	"time"
)

// Status is a cached startup verdict. Reading it never modifies networking.
type Status struct {
	Available bool   `json:"available"`
	Backend   string `json:"backend"`
	Reason    string `json:"reason,omitempty"`
	Probe     Probe  `json:"probe"`
}

type Probe struct {
	Attempted  bool   `json:"attempted"`
	Stage      string `json:"stage"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

type endpoint interface {
	Wrap(*exec.Cmd)
	Gateway() string
	Close(context.Context) error
}

type backend interface {
	Create(context.Context) (endpoint, error)
	Close(context.Context) error
}

type acquisition struct {
	done  chan struct{}
	lease *attachment
	err   error
}

// Pool owns named network allocations. Keys are opaque caller-owned identities.
// allocationMu serializes address selection without blocking existing networks.
type Pool struct {
	needsProbe   bool
	startErr     error
	mu           sync.Mutex
	allocationMu sync.Mutex
	closeMu      sync.Mutex
	report       Status
	backend      backend
	allocations  map[string]*attachment
	pending      map[string]*acquisition
	closed       bool
}

// New constructs a pool. Start probes the backend with a disposable namespace;
// callers decide whether an unavailable backend permits their workload to run.
func New(_ context.Context) *Pool {
	return &Pool{needsProbe: true, report: Status{Backend: "shared", Reason: "not started"}, allocations: make(map[string]*attachment), pending: make(map[string]*acquisition)}
}

func probePool(ctx context.Context) *Pool {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	b, probe := probeBackend(ctx)
	report := Status{Available: b != nil && probe.Error == "", Backend: "shared", Probe: probe}
	report.Probe.DurationMS = time.Since(started).Milliseconds()
	if report.Available {
		report.Backend = "netns"
	} else {
		report.Reason = probe.Error
	}
	log.Printf("network: enabled=%t backend=%s stage=%s reason=%q", report.Available, report.Backend, probe.Stage, report.Reason)
	return &Pool{report: report, backend: b, allocations: make(map[string]*attachment), pending: make(map[string]*acquisition)}
}

func (m *Pool) Status() Status {
	if m == nil {
		return Status{Backend: "shared", Reason: "network pool not configured"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.report
}

// Attachment identifies one allocation, not a lookup by a reusable key.
// Enter wraps namespace entry; the caller owns final process credentials.
type Attachment interface {
	Enter(*exec.Cmd) error
	Gateway() string
	Close(context.Context) error
}

type attachment struct {
	mu       sync.Mutex
	manager  *Pool
	key      string
	endpoint endpoint
	active   bool
}

func (m *Pool) Acquire(ctx context.Context, key string) (Attachment, error) {
	if m == nil || !m.Status().Available {
		return nil, ErrUnavailable
	}
	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, errors.New("network: manager closed")
		}
		if old := m.allocations[key]; old != nil {
			m.mu.Unlock()
			old.mu.Lock()
			active := old.active
			old.mu.Unlock()
			if active && old.current() {
				return old, nil
			}
			if err := old.Close(ctx); err != nil {
				return nil, err
			}
			continue
		}
		if pending := m.pending[key]; pending != nil {
			m.mu.Unlock()
			select {
			case <-pending.done:
				return pending.lease, pending.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		pending := &acquisition{done: make(chan struct{})}
		m.pending[key] = pending
		m.mu.Unlock()

		createCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		m.allocationMu.Lock()
		ep, err := m.backend.Create(createCtx)
		m.allocationMu.Unlock()
		cancel()
		if err != nil {
			err = fmt.Errorf("network: create allocation %s: %w", key, err)
		}

		m.mu.Lock()
		closed := m.closed
		if err == nil && !closed {
			pending.lease = &attachment{manager: m, key: key, endpoint: ep, active: true}
			m.allocations[key] = pending.lease
		} else if err == nil {
			pending.err = errors.New("network: manager closed")
		} else {
			pending.err = err
		}
		m.mu.Unlock()

		if closed && ep != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			pending.err = errors.Join(pending.err, ep.Close(cleanupCtx))
			cleanupCancel()
		}
		m.mu.Lock()
		if m.pending[key] == pending {
			delete(m.pending, key)
		}
		close(pending.done)
		m.mu.Unlock()
		if pending.err != nil {
			return nil, pending.err
		}
		log.Printf("network: attached key=%s", key)
		return pending.lease, nil
	}
}

func (a *attachment) current() bool {
	a.manager.mu.Lock()
	defer a.manager.mu.Unlock()
	return !a.manager.closed && a.manager.allocations[a.key] == a
}

func (a *attachment) Enter(cmd *exec.Cmd) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.active || !a.current() {
		return fmt.Errorf("network: allocation %s allocation is not active", a.key)
	}
	a.endpoint.Wrap(cmd)
	return nil
}

func (a *attachment) Gateway() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.active || !a.current() {
		return ""
	}
	return a.endpoint.Gateway()
}

func (a *attachment) Close(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.manager.mu.Lock()
	current := a.manager.allocations[a.key] == a
	a.manager.mu.Unlock()
	if !current {
		a.active = false
		return nil
	}
	a.active = false
	if err := a.endpoint.Close(ctx); err != nil {
		return fmt.Errorf("network: release allocation %s: %w", a.key, err)
	}
	a.manager.mu.Lock()
	if a.manager.allocations[a.key] == a {
		delete(a.manager.allocations, a.key)
	}
	a.manager.mu.Unlock()
	log.Printf("network: detached key=%s", a.key)
	return nil
}

func (m *Pool) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.closeMu.Lock()
	defer m.closeMu.Unlock()
	m.mu.Lock()
	m.closed = true
	pending := make([]<-chan struct{}, 0, len(m.pending))
	for _, acquisition := range m.pending {
		pending = append(pending, acquisition.done)
	}
	m.mu.Unlock()
	for _, done := range pending {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.mu.Lock()
	allocations := make([]*attachment, 0, len(m.allocations))
	for _, lease := range m.allocations {
		allocations = append(allocations, lease)
	}
	m.mu.Unlock()
	var err error
	for _, lease := range allocations {
		if closeErr := lease.Close(ctx); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}
	if m.backend != nil {
		m.allocationMu.Lock()
		err = errors.Join(err, m.backend.Close(ctx))
		m.allocationMu.Unlock()
	}
	return err
}

func (m *Pool) Start(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("network: pool closed")
	}
	if !m.needsProbe {
		return m.startErr
	}
	p := probePool(ctx)
	m.backend, m.report, m.needsProbe = p.backend, p.report, false
	if p.backend != nil && !p.report.Available {
		m.startErr = fmt.Errorf("network probe cleanup remains pending: %s", p.report.Reason)
	}
	return m.startErr
}
