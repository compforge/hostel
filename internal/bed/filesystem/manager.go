// Package filesystem owns BedFS preparation and file isolation resources.
package filesystem

import (
	"context"
	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"os"
	"path/filepath"
	"sync"
)

type Manager struct {
	bed.Noop
	mu       sync.Mutex
	files    map[*bed.Bed]*bedfs.FS
	isolator isolation.Isolator
	status   bed.StatusWriter[bed.FilesystemStatus]
}

func NewManager(iso isolation.Isolator, status bed.StatusWriter[bed.FilesystemStatus]) *Manager {
	return &Manager{files: make(map[*bed.Bed]*bedfs.FS), isolator: iso, status: status}
}
func (m *Manager) Prepare(_ context.Context, b *bed.Bed) error {
	if m.Files(b) != nil {
		return nil
	}
	home := filepath.Join(b.Spec().Dir, "data")
	if err := os.MkdirAll(filepath.Join(home, "workspace"), 0755); err != nil {
		return err
	}
	fs, err := bedfs.New(home)
	if err != nil {
		return err
	}
	// Publish the cleanup owner before potentially partial isolation preparation.
	m.mu.Lock()
	m.files[b] = fs
	m.mu.Unlock()
	if p, ok := m.isolator.(isolation.Preparer); ok {
		if err := p.Prepare(fs); err != nil {
			return err
		}
	}
	m.status.Set(b, bed.FilesystemStatus{Home: fs.Home(), Workspace: fs.Workspace(), Prepared: true})
	return nil
}
func (m *Manager) Files(b *bed.Bed) *bedfs.FS { m.mu.Lock(); defer m.mu.Unlock(); return m.files[b] }
func (m *Manager) Release(_ context.Context, b *bed.Bed) error {
	fs := m.Files(b)
	if fs == nil {
		return nil
	}
	if err := fs.Close(); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.files, b)
	m.mu.Unlock()
	m.status.Update(b, func(s *bed.FilesystemStatus) { s.Prepared = false })
	return nil
}

type Status struct {
	Requested     string                           `json:"requested"`
	Effective     string                           `json:"effective"`
	Ceiling       string                           `json:"ceiling"`
	Mechanism     string                           `json:"mechanism"`
	WorkspaceView isolation.WorkspaceViewReport    `json:"workspace_view"`
	System        isolation.SystemFacts            `json:"system"`
	Probes        map[string]isolation.ProbeReport `json:"probes"`
}

func (m *Manager) Status() Status {
	view := Status{
		Effective:     m.isolator.Level().String(),
		Mechanism:     m.isolator.Name(),
		WorkspaceView: isolation.WorkspaceViewReport{Mode: "carrier", Available: true},
		Probes:        map[string]isolation.ProbeReport{},
	}
	if m.isolator.WorkspaceMounted() {
		view.WorkspaceView.Mode = "mount"
	}
	if report, ok := m.isolator.(isolation.Report); ok {
		details := report.Diagnostics()
		view.Requested = report.Requested().String()
		view.Effective = report.Effective().String()
		view.Ceiling = report.Ceiling().String()
		view.Mechanism = report.Mechanism()
		view.WorkspaceView = report.WorkspaceView()
		view.System = details.System
		view.Probes = details.Probes
	}
	return view
}

var _ bed.Component[Status] = (*Manager)(nil)
