package privilege

import (
	"context"
	"sync"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
)

// Manager owns privilege facts and local Unix identity allocations. Only the
// lifecycle owner decides when a local identity can be forgotten.
type Manager struct {
	bed.Noop
	status    bed.StatusWriter[bed.PrivilegeStatus]
	files     func(*bed.Bed) *bedfs.FS
	allocator *BedUserAllocator
	report    Report
	mu        sync.Mutex
	users     map[uint64]BedUser
	recovered map[uint64]bool
}

func NewManager(policy BedUserReport, fixed BedUser, effectiveCaps uint64, status bed.StatusWriter[bed.PrivilegeStatus], files func(*bed.Bed) *bedfs.FS) (*Manager, error) {
	allocator := NewFixedBedUserAllocator(fixed)
	if policy.Strategy == "per_bed" {
		var err error
		allocator, err = NewPerBedUserAllocator(policy.UIDMin, policy.UIDMax)
		if err != nil {
			return nil, err
		}
	}
	return &Manager{allocator: allocator, report: NewReport(policy, effectiveCaps), users: make(map[uint64]BedUser), recovered: make(map[uint64]bool), status: status, files: files}, nil
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

func (m *Manager) Recover(_ context.Context, b *bed.Bed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recovered[b.LocalID] = true
	for _, dir := range b.Spec().RecoveryDirs {
		if err := m.allocator.ReserveOwnedDirectory(b.ID, dir); err != nil {
			return err
		}
	}
	return nil
}
func (m *Manager) Prepare(_ context.Context, b *bed.Bed) error {
	m.mu.Lock()
	user, ok := m.users[b.LocalID]
	if !ok {
		var err error
		user, err = m.allocator.Acquire(b.ID)
		if err != nil {
			m.mu.Unlock()
			return err
		}
		m.users[b.LocalID] = user
	}
	m.mu.Unlock()
	m.status.Set(b, bed.PrivilegeStatus{UID: uint32(user.UID()), GID: uint32(user.GID()), Assigned: true})
	if m.files != nil {
		if fs := m.files(b); fs != nil {
			return user.Prepare(fs)
		}
	}
	return nil
}
func (m *Manager) User(b *bed.Bed) BedUser {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.users[b.LocalID]
}
func (m *Manager) Forget(_ context.Context, b *bed.Bed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Bed Manager fences the logical ID until this exact LocalID is forgotten.
	// Repeated Forget must never release an allocation created after it.
	if _, ok := m.users[b.LocalID]; ok {
		m.allocator.Release(b.ID)
		delete(m.users, b.LocalID)
	} else {
		// Recovery reserves ownership before Prepare allocates a runtime user.
		if _, ok := m.recovered[b.LocalID]; ok {
			m.allocator.Release(b.ID)
		}
	}
	delete(m.recovered, b.LocalID)
	m.status.Set(b, bed.PrivilegeStatus{})
	return nil
}

var _ bed.Component[Report] = (*Manager)(nil)
