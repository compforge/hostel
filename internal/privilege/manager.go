package privilege

import (
	"context"
	"sync"

	"github.com/qiankunli/hostel/internal/lifecycle"
)

// Manager owns privilege facts and local Unix identity allocations. Only the
// lifecycle owner decides when a local identity can be forgotten.
type Manager struct {
	allocator *BedUserAllocator
	report    Report
	mu        sync.Mutex
	bindings  map[string]*Binding
}

func NewManager(policy BedUserReport, fixed BedUser, effectiveCaps uint64) (*Manager, error) {
	allocator := NewFixedBedUserAllocator(fixed)
	if policy.Strategy == "per_bed" {
		var err error
		allocator, err = NewPerBedUserAllocator(policy.UIDMin, policy.UIDMax)
		if err != nil {
			return nil, err
		}
	}
	return &Manager{allocator: allocator, report: NewReport(policy, effectiveCaps), bindings: make(map[string]*Binding)}, nil
}
func (m *Manager) Diagnostics() Report {
	report := m.report
	report.Requirements.Capabilities = append([]string(nil), report.Requirements.Capabilities...)
	report.Requirements.MissingCapabilities = append([]string(nil), report.Requirements.MissingCapabilities...)
	m.allocator.mu.Lock()
	report.ReservedUsers = len(m.allocator.byBed)
	m.allocator.mu.Unlock()
	return report
}

// Bind returns the current local identity's privilege participant. Directory
// names are supplied by Bed Manager, which owns workspace layout and recovery.
func (m *Manager) Bind(id string, directories []string) *Binding {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b := m.bindings[id]; b != nil {
		return b
	}
	b := &Binding{manager: m, id: id, directories: append([]string(nil), directories...)}
	m.bindings[id] = b
	return b
}

type Binding struct {
	lifecycle.Noop
	manager     *Manager
	id          string
	directories []string
	user        BedUser
}

var _ lifecycle.Component[Report] = (*Binding)(nil)

func (b *Binding) Recover(context.Context) error {
	for _, dir := range b.directories {
		if err := b.manager.allocator.ReserveOwnedDirectory(b.id, dir); err != nil {
			return err
		}
	}
	return nil
}
func (b *Binding) Prepare(context.Context) error {
	user, err := b.manager.allocator.Acquire(b.id)
	if err == nil {
		b.user = user
	}
	return err
}
func (b *Binding) User() BedUser { return b.user }
func (b *Binding) Forget(context.Context) error {
	m := b.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bindings[b.id] != b {
		return nil
	}
	m.allocator.Release(b.id)
	delete(m.bindings, b.id)
	return nil
}
func (b *Binding) Diagnostics() Report { return b.manager.Diagnostics() }
