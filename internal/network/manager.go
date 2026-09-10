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

// Manager is shared by all Beds. Disabled managers leave commands untouched;
// enabled managers never silently bypass a failed Bed network allocation.
type Manager struct {
	mu      sync.Mutex
	report  Report
	backend backend
	beds    map[string]*attachment
	closed  bool
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
	return &Manager{report: report, backend: b, beds: make(map[string]*attachment)}
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
	manager  *Manager
	bedID    string
	endpoint endpoint
	active   bool
}

func (m *Manager) Acquire(ctx context.Context, bedID string) (Attachment, error) {
	if m == nil || !m.report.Enabled {
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("network: manager closed")
	}
	if old := m.beds[bedID]; old != nil {
		if old.active {
			return old, nil
		}
		// A failed release retains cleanup ownership, never service eligibility.
		if err := old.closeLocked(ctx); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ep, err := m.backend.Create(ctx)
	if err != nil {
		return nil, fmt.Errorf("network: create bed %s: %w", bedID, err)
	}
	lease := &attachment{manager: m, bedID: bedID, endpoint: ep, active: true}
	m.beds[bedID] = lease
	log.Printf("network: attached bed=%s", bedID)
	return lease, nil
}

func (a *attachment) Enter(cmd *exec.Cmd) error {
	a.manager.mu.Lock()
	defer a.manager.mu.Unlock()
	if !a.active || a.manager.closed || a.manager.beds[a.bedID] != a {
		return fmt.Errorf("network: bed %s allocation is not active", a.bedID)
	}
	a.endpoint.Wrap(cmd)
	return nil
}

func (a *attachment) Gateway() string {
	a.manager.mu.Lock()
	defer a.manager.mu.Unlock()
	if !a.active || a.manager.closed {
		return ""
	}
	return a.endpoint.Gateway()
}

func (a *attachment) Close(ctx context.Context) error {
	a.manager.mu.Lock()
	defer a.manager.mu.Unlock()
	return a.closeLocked(ctx)
}

func (a *attachment) closeLocked(ctx context.Context) error {
	a.active = false
	// An old handle can never release a replacement allocation.
	if a.manager.beds[a.bedID] != a {
		return nil
	}
	if err := a.endpoint.Close(ctx); err != nil {
		return fmt.Errorf("network: release bed %s: %w", a.bedID, err)
	}
	delete(a.manager.beds, a.bedID)
	log.Printf("network: detached bed=%s", a.bedID)
	return nil
}

func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	var err error
	for id, ep := range m.beds {
		if e := ep.closeLocked(ctx); e != nil {
			err = errors.Join(err, fmt.Errorf("bed %s: %w", id, e))
		} else {
			delete(m.beds, id)
		}
	}
	if m.backend != nil {
		err = errors.Join(err, m.backend.Close(ctx))
	}
	return err
}
