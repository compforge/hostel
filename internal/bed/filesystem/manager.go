// Package filesystem owns BedFS preparation and file isolation resources.
package filesystem

import (
	"context"
	"log"
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
	// Temporary files are Bed data too: separate executions and Services must
	// share this directory, not receive an unrelated per-process tmpfs.
	if err := fs.EnsureDir(filepath.Join(home, "tmp")); err != nil {
		return err
	}
	// API mapping ownership survives an unavailable process-path mechanism.
	// Report the gap once at preparation; do not reject otherwise usable Beds.
	support := m.isolator.View(fs).MappingSupport()
	for _, mapping := range fs.PathMappings() {
		if !support.Supports(mapping.ReadOnly) {
			log.Printf("filesystem: bed=%s path=%s mapping available through API; process mapping unavailable read_only=%t", b.ID.String(), mapping.BedPath, mapping.ReadOnly)
		}
	}
	if p, ok := m.isolator.(isolation.Preparer); ok {
		if err := p.Prepare(fs); err != nil {
			return err
		}
	}
	m.status.Set(b, bed.FilesystemStatus{Rootfs: fs.Rootfs(), Workdir: fs.Workdir(), Prepared: true})
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
	Features    map[string]feature.Status        `json:"features"`
	Requested   string                           `json:"requested"`
	Effective   string                           `json:"effective"`
	Ceiling     string                           `json:"ceiling"`
	Supported   []string                         `json:"supported"`
	Mechanism   string                           `json:"mechanism"`
	ProcessView isolation.ProcessViewReport      `json:"process_view"`
	Probes      map[string]hostfacts.ProbeReport `json:"probes"`
}

func (m *Manager) Status() Status {
	view := Status{
		Effective:   m.isolator.Level().String(),
		Mechanism:   m.isolator.Name(),
		ProcessView: isolation.ProcessViewReport{Mode: "carrier", Available: true},
		Probes:      map[string]hostfacts.ProbeReport{},
	}
	if m.isolator.WorkdirMounted() {
		view.ProcessView.Mode = "mount"
	}
	if report, ok := m.isolator.(isolation.Report); ok {
		details := report.Diagnostics()
		view.Requested = report.Requested().String()
		view.Effective = report.Effective().String()
		view.Ceiling = report.Ceiling().String()
		view.Mechanism = report.Mechanism()
		view.ProcessView = report.ProcessView()
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
