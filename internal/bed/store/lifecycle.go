package store

import (
	"context"
	"github.com/qiankunli/hostel/internal/bed"
)

func (m *Manager) SetStatusWriter(status bed.StatusWriter[bed.StoreStatus]) { m.status = status }
func (m *Manager) ObserveStage(b *bed.Bed, observe func(StageInStep)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.observers == nil {
		m.observers = make(map[*bed.Bed]func(StageInStep))
	}
	m.observers[b] = observe
}
func (m *Manager) Prepare(ctx context.Context, b *bed.Bed) error {
	spec := b.Spec()
	m.mu.Lock()
	observer := m.observers[b]
	if m.stages == nil {
		m.stages = make(map[*bed.Bed]StageInResult)
	}
	m.stages[b] = StageInResult{}
	m.mu.Unlock()
	result, err := m.StageInBedFS(ctx, spec.Sync, StageInRequest{
		BedID: b.ID, BedDir: spec.Dir, LocalPresent: spec.LocalPresent,
		LocalGeneration: spec.LocalGeneration, OnStep: observer,
	})
	m.mu.Lock()
	if m.stages == nil {
		m.stages = make(map[*bed.Bed]StageInResult)
	}
	m.stages[b] = result
	delete(m.observers, b)
	m.mu.Unlock()
	status := bed.StoreStatus{Source: string(result.Source), Restored: result.Restored}
	if result.Snapshot != nil {
		status.Generation = result.Snapshot.Generation
		status.SnapshotBytes = result.Snapshot.Bytes
	}
	m.status.Set(b, status)
	return err
}
func (m *Manager) StageResult(b *bed.Bed) StageInResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stages[b]
}
func (m *Manager) Stop(ctx context.Context, b *bed.Bed) error {
	m.mu.Lock()
	_, owned := m.stages[b]
	m.mu.Unlock()
	if !owned {
		return nil
	}
	return m.StopTransfers(ctx, b.ID)
}
func (m *Manager) Release(_ context.Context, b *bed.Bed) error {
	m.mu.Lock()
	delete(m.stages, b)
	delete(m.observers, b)
	m.mu.Unlock()
	return nil
}
func (m *Manager) Close(ctx context.Context) error { return m.StopTransfers(ctx, "") }

var _ bed.Component[Report] = (*Manager)(nil)

func (m *Manager) PersistBed(ctx context.Context, b *bed.Bed, generation int64) error {
	spec := b.Spec()
	if err := m.Persist(ctx, spec.Sync, b.ID, spec.Dir, generation); err != nil {
		return err
	}
	m.status.Update(b, func(s *bed.StoreStatus) { s.Generation = generation })
	return nil
}
func (m *Manager) StatBed(ctx context.Context, b *bed.Bed) (*SnapshotInfo, error) {
	snapshot, err := m.Stat(ctx, b.Spec().Sync, b.ID)
	if err == nil && snapshot != nil {
		m.status.Update(b, func(s *bed.StoreStatus) { s.Generation = snapshot.Generation; s.SnapshotBytes = snapshot.Bytes })
	}
	return snapshot, err
}
