package executor

import (
	"context"
	"fmt"
	"github.com/qiankunli/hostel/internal/bed"
	"sync"
)

type Report struct {
	Backend string `json:"backend"`
}

func Describe(factory Factory) Report { return Report{Backend: factory.Backend()} }

// Manager owns the replaceable process realm for each Bed allocation.
type Manager struct {
	bed.Noop
	mu      sync.Mutex
	realms  map[*bed.Bed]*realm
	factory Factory
	status  bed.StatusWriter[bed.ExecutorStatus]
}
type realm struct {
	mu       sync.Mutex
	executor Executor
	stopped  bool
}

func NewManager(factory Factory, status bed.StatusWriter[bed.ExecutorStatus]) *Manager {
	return &Manager{factory: factory, status: status, realms: make(map[*bed.Bed]*realm)}
}
func (m *Manager) Prepare(_ context.Context, b *bed.Bed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.realms[b] == nil {
		m.realms[b] = &realm{}
	}
	return nil
}
func (m *Manager) slot(b *bed.Bed) *realm { m.mu.Lock(); defer m.mu.Unlock(); return m.realms[b] }
func (m *Manager) Current(b *bed.Bed) Executor {
	r := m.slot(b)
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.executor
}
func (m *Manager) For(ctx context.Context, b *bed.Bed) (Executor, error) {
	r := m.slot(b)
	if r == nil {
		return nil, fmt.Errorf("executor: Bed %s is not prepared", b.ID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return nil, fmt.Errorf("executor: Bed %s is stopped", b.ID)
	}
	if current := r.executor; current != nil {
		if current.State() == StateReady {
			return current, nil
		}
		// Never overlap replacement with unresolved cleanup of the previous realm.
		if err := current.Shutdown(ctx); err != nil {
			return nil, fmt.Errorf("bed: previous executor cleanup: %w", err)
		}
	}
	created, err := m.factory.Create(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	r.executor = created
	m.status.Set(b, bed.ExecutorStatus{ID: created.ID(), Backend: created.Backend(), State: string(created.State())})
	return created, nil
}
func (m *Manager) Stop(ctx context.Context, b *bed.Bed) error {
	r := m.slot(b)
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	if r.executor != nil {
		if err := r.executor.Shutdown(ctx); err != nil {
			return err
		}
		r.executor = nil
	}
	m.status.Set(b, bed.ExecutorStatus{Backend: m.factory.Backend(), State: string(StateStopped)})
	return nil
}
func (m *Manager) Release(_ context.Context, b *bed.Bed) error {
	m.mu.Lock()
	delete(m.realms, b)
	m.mu.Unlock()
	return nil
}
func (m *Manager) Close(context.Context) error { return m.factory.Close() }
func (m *Manager) Diagnostics() Report         { return Describe(m.factory) }

var _ bed.Component[Report] = (*Manager)(nil)
