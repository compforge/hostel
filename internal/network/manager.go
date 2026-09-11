// Package network owns optional, per-Bed network namespaces. It does not own
// the Bed's durable data or credentials.
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

// Report is a cached startup verdict. Reading it never modifies networking.
type Report struct {
	Enabled bool   `json:"enabled"`
	Backend string `json:"backend"`
	Scope   string `json:"scope"`
	Reason  string `json:"reason,omitempty"`
	Probe   Probe  `json:"probe"`
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

// Manager is shared by all Beds. Its state lock is never held across kernel or
// DNS operations. allocationMu serializes address selection and namespace
// creation without blocking an existing Bed's network operations.
type Manager struct {
	mu           sync.Mutex
	allocationMu sync.Mutex
	closeMu      sync.Mutex
	report       Report
	backend      backend
	beds         map[string]*attachment
	pending      map[string]*acquisition
	closed       bool
}

// New probes the complete backend using a disposable namespace. Probe failure
// disables only networking, leaving ordinary Hostel services available.
func New(ctx context.Context) *Manager {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	b, probe := probeBackend(ctx)
	report := Report{Enabled: b != nil, Backend: "shared", Scope: "carrier", Probe: probe}
	report.Probe.DurationMS = time.Since(started).Milliseconds()
	if b != nil {
		report.Backend, report.Scope = "netns", "bed_processes"
	} else {
		report.Reason = probe.Error
	}
	log.Printf("network: enabled=%t backend=%s stage=%s reason=%q", report.Enabled, report.Backend, probe.Stage, report.Reason)
	return &Manager{report: report, backend: b, beds: make(map[string]*attachment), pending: make(map[string]*acquisition)}
}

func (m *Manager) Report() Report {
	if m == nil {
		return Report{Backend: "shared", Scope: "carrier", Reason: "network manager not configured"}
	}
	return m.report
}

// Attachment is one allocation, not a lookup by a reusable Bed ID. Enter only
// enters its network; isolation.Environment owns final privilege dropping.
type Attachment interface {
	Enter(*exec.Cmd) error
	Gateway() string
	Close(context.Context) error
}

type attachment struct {
	mu       sync.Mutex
	manager  *Manager
	bedID    string
	endpoint endpoint
	active   bool
}

func (m *Manager) Acquire(ctx context.Context, bedID string) (Attachment, error) {
	if m == nil || !m.report.Enabled {
		return nil, nil
	}
	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, errors.New("network: manager closed")
		}
		if old := m.beds[bedID]; old != nil {
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
		if pending := m.pending[bedID]; pending != nil {
			m.mu.Unlock()
			select {
			case <-pending.done:
				return pending.lease, pending.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		pending := &acquisition{done: make(chan struct{})}
		m.pending[bedID] = pending
		m.mu.Unlock()

		createCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		m.allocationMu.Lock()
		ep, err := m.backend.Create(createCtx)
		m.allocationMu.Unlock()
		cancel()
		if err != nil {
			err = fmt.Errorf("network: create bed %s: %w", bedID, err)
		}

		m.mu.Lock()
		closed := m.closed
		if err == nil && !closed {
			pending.lease = &attachment{manager: m, bedID: bedID, endpoint: ep, active: true}
			m.beds[bedID] = pending.lease
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
		if m.pending[bedID] == pending {
			delete(m.pending, bedID)
		}
		close(pending.done)
		m.mu.Unlock()
		if pending.err != nil {
			return nil, pending.err
		}
		log.Printf("network: attached bed=%s", bedID)
		return pending.lease, nil
	}
}

func (a *attachment) current() bool {
	a.manager.mu.Lock()
	defer a.manager.mu.Unlock()
	return !a.manager.closed && a.manager.beds[a.bedID] == a
}

func (a *attachment) Enter(cmd *exec.Cmd) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.active || !a.current() {
		return fmt.Errorf("network: bed %s allocation is not active", a.bedID)
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
	current := a.manager.beds[a.bedID] == a
	a.manager.mu.Unlock()
	if !current {
		a.active = false
		return nil
	}
	a.active = false
	if err := a.endpoint.Close(ctx); err != nil {
		return fmt.Errorf("network: release bed %s: %w", a.bedID, err)
	}
	a.manager.mu.Lock()
	if a.manager.beds[a.bedID] == a {
		delete(a.manager.beds, a.bedID)
	}
	a.manager.mu.Unlock()
	log.Printf("network: detached bed=%s", a.bedID)
	return nil
}

func (m *Manager) Close(ctx context.Context) error {
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
	beds := make([]*attachment, 0, len(m.beds))
	for _, lease := range m.beds {
		beds = append(beds, lease)
	}
	m.mu.Unlock()
	var err error
	for _, lease := range beds {
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
