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
	beds    map[string]endpoint
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
	return &Manager{report: report, backend: b, beds: make(map[string]endpoint)}
}

func (m *Manager) Report() Report {
	if m == nil {
		return Report{Backend: "shared", Scope: "carrier", Reason: "network manager not configured"}
	}
	return m.report
}

func (m *Manager) Acquire(ctx context.Context, bedID string) error {
	if m == nil || !m.report.Enabled {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("network: manager closed")
	}
	if _, ok := m.beds[bedID]; ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ep, err := m.backend.Create(ctx)
	if err != nil {
		return fmt.Errorf("network: create bed %s: %w", bedID, err)
	}
	m.beds[bedID] = ep
	log.Printf("network: attached bed=%s", bedID)
	return nil
}

// Wrap is called at the Executor boundary for both commands and session shells.
func (m *Manager) Wrap(bedID string, cmd *exec.Cmd) error {
	if m == nil || !m.report.Enabled {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ep, ok := m.beds[bedID]
	if !ok || m.closed {
		return fmt.Errorf("network: bed %s has no active network", bedID)
	}
	ep.Wrap(cmd)
	return nil
}

func (m *Manager) Gateway(bedID string) string {
	if m == nil || !m.report.Enabled {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ep := m.beds[bedID]; ep != nil {
		return ep.Gateway()
	}
	return ""
}

// Release follows process shutdown. Failed cleanup stays owned for Close to retry.
func (m *Manager) Release(ctx context.Context, bedID string) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ep := m.beds[bedID]
	if ep == nil {
		return nil
	}
	if err := ep.Close(ctx); err != nil {
		return fmt.Errorf("network: release bed %s: %w", bedID, err)
	}
	delete(m.beds, bedID)
	log.Printf("network: detached bed=%s", bedID)
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
		if e := ep.Close(ctx); e != nil {
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
