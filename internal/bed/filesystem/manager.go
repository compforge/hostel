// Package filesystem owns BedFS preparation and file isolation resources.
package filesystem

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/feature"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
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
	if err := fs.SetPathMappings(b.Spec().PathMappings); err != nil {
		return err
	}
	if len(fs.PathMappings()) > 0 {
		status := m.Status()
		if status.WorkspaceView.Mode == "carrier" {
			return fmt.Errorf("filesystem: path mappings require a process path view")
		}
		if status.Mechanism == "landlock" {
			return fmt.Errorf("filesystem: external path mappings are unavailable with Landlock")
		}
		for _, mapping := range fs.PathMappings() {
			if mapping.ReadOnly && !m.isolator.WorkspaceMounted() {
				return fmt.Errorf("filesystem: read-only path mappings require a mount view")
			}
		}
	}
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
	Features      map[string]feature.Status        `json:"features"`
	Requested     string                           `json:"requested"`
	Effective     string                           `json:"effective"`
	Ceiling       string                           `json:"ceiling"`
	Supported     []string                         `json:"supported"`
	Mechanism     string                           `json:"mechanism"`
	WorkspaceView isolation.WorkspaceViewReport    `json:"workspace_view"`
	Probes        map[string]hostfacts.ProbeReport `json:"probes"`
}

func (m *Manager) Status() Status {
	view := Status{
		Effective:     m.isolator.Level().String(),
		Mechanism:     m.isolator.Name(),
		WorkspaceView: isolation.WorkspaceViewReport{Mode: "carrier", Available: true},
		Probes:        map[string]hostfacts.ProbeReport{},
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
		view.Probes = details.Probes
		view.Features = details.Features
	}
	for _, level := range m.LevelStatus().Supported {
		view.Supported = append(view.Supported, level.(isolation.Level).String())
	}
	return view
}

func (m *Manager) LevelStatus() bed.LevelStatus {
	confined, private := m.isolator.Level() == isolation.Confined, m.isolator.Level() == isolation.Private
	if report, ok := m.isolator.(isolation.Report); ok {
		features := report.Diagnostics().Features
		confined = confined || features["landlock"].Probe == "available" || features["uid"].Probe == "available"
		private = private || features["bwrap"].Probe == "available"
	}
	levels := []bed.Level{isolation.Shared}
	if confined {
		levels = append(levels, isolation.Confined)
	}
	if private {
		levels = append(levels, isolation.Private)
	}
	return bed.LevelStatus{Supported: levels}
}

var _ bed.Component[Status] = (*Manager)(nil)
