package privilege

import (
	"context"
	"fmt"
	"slices"
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
	leases    map[*bed.Bed]*BedUserLease
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
	return &Manager{allocator: allocator, report: NewReport(policy, effectiveCaps), leases: make(map[*bed.Bed]*BedUserLease), status: status, files: files}, nil
}
func (m *Manager) Status() Status {
	report := m.report
	report.BedUser.SupplementaryGIDs = slices.Clone(report.BedUser.SupplementaryGIDs)
	report.Selection.Policy.SupplementaryGIDs = slices.Clone(report.Selection.Policy.SupplementaryGIDs)
	report.Selection.Supported = append([]Level(nil), report.Selection.Supported...)
	report.Requirements.Capabilities = append([]string(nil), report.Requirements.Capabilities...)
	report.Requirements.MissingCapabilities = append([]string(nil), report.Requirements.MissingCapabilities...)
	m.allocator.mu.Lock()
	report.ReservedUsers = len(m.allocator.byBed)
	m.allocator.mu.Unlock()
	return report
}

// SetSelection binds the immutable startup choice before any Bed is prepared.
func (m *Manager) SetSelection(s Selection) {
	s.Supported = append([]Level(nil), s.Supported...)
	m.report.Selection = s
}

// Reserve selects a lease before the lifecycle owner persists it. Prepare must
// not allocate: persistence must precede any use by files, processes or Network.
func (m *Manager) Reserve(b *bed.Bed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	lease, err := m.allocator.Acquire(b.ID.String())
	if err != nil {
		return err
	}
	m.leases[b] = lease
	return nil
}

// Restore holds every known field; Reserve completes the lease after all
// persisted identities have been restored by the lifecycle owner.
func (m *Manager) Restore(b *bed.Bed, uid, gid *int) error {
	return m.allocator.Restore(b.ID.String(), uid, gid)
}
func (m *Manager) Identity(b *bed.Bed) (Identity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lease := m.leases[b]
	if lease == nil {
		return Identity{}, fmt.Errorf("privilege: bed %s has no identity lease", b.ID)
	}
	return Identity{UID: lease.UID(), GID: lease.GID()}, nil
}
func (m *Manager) Prepare(_ context.Context, b *bed.Bed) error {
	if _, err := m.Identity(b); err != nil {
		return err
	}
	user := m.User(b)
	m.status.Set(b, bed.PrivilegeStatus{UID: uint32(user.UID()), GID: uint32(user.GID()), Assigned: true, SupplementaryGIDs: user.SupplementaryGIDs()})
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
	if lease := m.leases[b]; lease != nil {
		return lease.user.withGroups(m.report.BedUser.SupplementaryGIDs)
	}
	return BedUser{}
}
func (m *Manager) Forget(_ context.Context, b *bed.Bed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if lease := m.leases[b]; lease != nil {
		lease.Release()
		delete(m.leases, b)
	}
	m.status.Set(b, bed.PrivilegeStatus{})
	return nil
}

var _ bed.Subsystem[Status] = (*Manager)(nil)
