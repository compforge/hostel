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
	report    Status
	mu        sync.Mutex
	users     map[bed.ID]BedUser
	recovered map[bed.ID]bool
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
	return &Manager{allocator: allocator, report: NewReport(policy, effectiveCaps), users: make(map[bed.ID]BedUser), recovered: make(map[bed.ID]bool), status: status, files: files}, nil
}
func (m *Manager) Status() Status {
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
	m.recovered[b.ID] = true
	for _, dir := range b.Spec().RecoveryDirs {
		if err := m.allocator.ReserveOwnedDirectory(b.ID.String(), dir); err != nil {
			return err
		}
	}
	return nil
}
func (m *Manager) Prepare(_ context.Context, b *bed.Bed) error {
	m.mu.Lock()
	user, ok := m.users[b.ID]
	if !ok {
		var err error
		user, err = m.allocator.Acquire(b.ID.String())
		if err != nil {
			m.mu.Unlock()
			return err
		}
		m.users[b.ID] = user
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
	return m.users[b.ID]
}
func (m *Manager) Forget(_ context.Context, b *bed.Bed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Bed Manager fences the logical ID until this exact LocalID is forgotten.
	// Repeated Forget must never release an allocation created after it.
	if _, ok := m.users[b.ID]; ok {
		m.allocator.Release(b.ID.String())
		delete(m.users, b.ID)
	} else {
		// Recovery reserves ownership before Prepare allocates a runtime user.
		if _, ok := m.recovered[b.ID]; ok {
			m.allocator.Release(b.ID.String())
		}
	}
	delete(m.recovered, b.ID)
	m.status.Set(b, bed.PrivilegeStatus{})
	return nil
}

var _ bed.Component[Status] = (*Manager)(nil)
