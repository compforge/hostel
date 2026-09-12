package executor

import (
	"context"
	"fmt"
	"github.com/qiankunli/hostel/internal/bed"
	"golang.org/x/sync/semaphore"
	"sync"
)

type Status struct {
	Backend string `json:"backend"`
}

func Describe(factory Factory) Status { return Status{Backend: factory.Backend()} }

// Manager owns the replaceable process realm for each Bed allocation.
type Manager struct {
	bed.Noop
	mu      sync.Mutex
	realms  map[*bed.Bed]*realm
	factory Factory
	status  bed.StatusWriter[bed.ExecutorStatus]
}
type realm struct {
	mu        *semaphore.Weighted
	executor  Executor
	stopped   bool
	observers []<-chan struct{}
}

func NewManager(factory Factory, status bed.StatusWriter[bed.ExecutorStatus]) *Manager {
	return &Manager{factory: factory, status: status, realms: make(map[*bed.Bed]*realm)}
}
func (m *Manager) Prepare(_ context.Context, b *bed.Bed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.realms[b] == nil {
		m.realms[b] = &realm{mu: semaphore.NewWeighted(1)}
	}
	return nil
}
func (m *Manager) slot(b *bed.Bed) *realm { m.mu.Lock(); defer m.mu.Unlock(); return m.realms[b] }
func (m *Manager) Current(b *bed.Bed) Executor {
	r := m.slot(b)
	if r == nil {
		return nil
	}
	_ = r.mu.Acquire(context.Background(), 1)
	defer r.mu.Release(1)
	return r.executor
}
func (m *Manager) For(ctx context.Context, b *bed.Bed) (Executor, error) {
	r := m.slot(b)
	if r == nil {
		return nil, fmt.Errorf("executor: Bed %s is not prepared", b.ID.String())
	}
	if err := r.mu.Acquire(ctx, 1); err != nil {
		return nil, err
	}
	defer r.mu.Release(1)
	if r.stopped {
		return nil, fmt.Errorf("executor: Bed %s is stopped", b.ID.String())
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
	created, err := m.factory.Create(ctx, b.ID.String())
	if err != nil {
		return nil, err
	}
	r.executor = created
	m.status.Set(b, bed.ExecutorStatus{ID: created.ID(), Backend: created.Backend(), State: string(created.State())})
	pending := r.observers[:0]
	for _, observed := range r.observers {
		select {
		case <-observed:
		default:
			pending = append(pending, observed)
		}
	}
	r.observers = pending
	done := make(chan struct{})
	r.observers = append(r.observers, done)
	go func() {
		defer close(done)
		<-created.Done()
		_ = r.mu.Acquire(context.Background(), 1)
		defer r.mu.Release(1)
		// An old realm may exit after replacement. Publish only for its exact
		// allocation, never into the new executor's status.
		if r.executor == created {
			m.status.Set(b, bed.ExecutorStatus{ID: created.ID(), Backend: created.Backend(), State: string(created.Exit().State)})
		}
	}()
	return created, nil
}
func (m *Manager) Stop(ctx context.Context, b *bed.Bed) error {
	r := m.slot(b)
	if r == nil {
		return nil
	}
	if err := r.mu.Acquire(ctx, 1); err != nil {
		return err
	}
	defer r.mu.Release(1)
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
func (m *Manager) Release(ctx context.Context, b *bed.Bed) error {
	r := m.slot(b)
	if r == nil {
		return nil
	}
	if err := r.mu.Acquire(ctx, 1); err != nil {
		return err
	}
	observers := append([]<-chan struct{}(nil), r.observers...)
	r.mu.Release(1)
	for _, done := range observers {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.mu.Lock()
	delete(m.realms, b)
	m.mu.Unlock()
	return nil
}
func (m *Manager) Close(context.Context) error { return m.factory.Close() }
func (m *Manager) Status() Status              { return Describe(m.factory) }

var _ bed.Component[Status] = (*Manager)(nil)
