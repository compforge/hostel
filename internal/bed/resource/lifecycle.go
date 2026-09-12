package resource

import (
	"context"
	"sync"

	"github.com/qiankunli/hostel/internal/bed"
)

// Manager owns accounting and the carrier admission sampler. Resource limits
// can be added here without changing Bed Manager's lifecycle contract.
type Manager struct {
	bed.Noop
	tracker     Tracker
	mu          sync.Mutex
	allocations map[*bed.Bed]bool
	admission   Admitter
	status      bed.StatusWriter[bed.ResourceStatus]
}

func NewManager(tracker Tracker, status bed.StatusWriter[bed.ResourceStatus]) *Manager {
	return &Manager{tracker: tracker, status: status, allocations: make(map[*bed.Bed]bool), admission: NoopAdmission("resource admission not configured")}
}

// Start owns host accounting initialization, including partial allocations.
// Bed Manager registers this component for cleanup before calling Start.
func (m *Manager) Start(ctx context.Context) error {
	if starter, ok := m.tracker.(interface{ Start(context.Context) error }); ok {
		return starter.Start(ctx)
	}
	return ctx.Err()
}
func (m *Manager) Prepare(_ context.Context, b *bed.Bed) error {
	m.mu.Lock()
	m.allocations[b] = true
	m.mu.Unlock()
	group, err := m.tracker.OpenGroup(b.ID.String())
	if err != nil {
		return err
	}
	if group != nil {
		if err := group.Close(); err != nil {
			return err
		}
	}
	m.status.Set(b, bed.ResourceStatus{Accounting: m.tracker.Report().Available})
	return nil
}
func (m *Manager) Release(_ context.Context, b *bed.Bed) error {
	m.mu.Lock()
	owned := m.allocations[b]
	m.mu.Unlock()
	if !owned {
		return nil
	}
	if err := m.tracker.Release(b.ID.String()); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.allocations, b)
	m.mu.Unlock()
	m.status.Set(b, bed.ResourceStatus{})
	return nil
}

type Status struct {
	Accounting Report          `json:"accounting"`
	Admission  AdmissionReport `json:"admission"`
}

func (m *Manager) Status() Status {
	return Status{Accounting: m.tracker.Report(), Admission: m.admission.Report()}
}

var _ bed.Component[Status] = (*Manager)(nil)

func (m *Manager) SetAdmission(a Admitter) { m.admission = a }
func (m *Manager) Run(ctx context.Context) error {
	if runnable, ok := m.admission.(bed.Runnable); ok {
		return runnable.Run(ctx)
	}
	<-ctx.Done()
	return nil
}

// Close releases tracker-owned descriptors after Bed teardown. Trackers with
// no daemon resources need not implement io.Closer.
func (m *Manager) Close(context.Context) error {
	if closer, ok := m.tracker.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}
