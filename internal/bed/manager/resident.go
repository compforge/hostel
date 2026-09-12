package manager

import (
	"context"
	"errors"
	"fmt"
	"github.com/qiankunli/go-stdx/filepathx"
	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/store"
	"golang.org/x/sync/semaphore"
	"time"
)

// initializeResidentBed privately prepares one allocation. Only the composite
// publishes Ready after all domain prerequisites and the initial policy pass.
func (m *Manager) initializeResidentBed(ctx context.Context, init *bedInitialization) (resolved *managedBed, retErr error) {
	b := &managedBed{cleanupMu: semaphore.NewWeighted(1), Bed: init.model, local: init.local, executors: m.executorManager,
		shells: make(map[string]*Shell), sessions: make(map[string]*Session), inflightByKind: make(map[OperationKind]int)}
	m.bindRuntimeLifecycle(b)
	trace := beginLifecycle(ctx, b.Name, lifecycleInitialize)
	defer func() {
		b.recordLifecycle(trace.finish(lifecycleResult(retErr), retErr))
		if retErr == nil {
			return
		}
		// Even a failed Prepare may own partial resources. Preserve its exact owner
		// if rollback fails, and keep local data/UID until explicit identity cleanup.
		if err := m.rollback(b); err != nil {
			m.mu.Lock()
			m.retirements[b.Name] = b
			m.mu.Unlock()
			retErr = errors.Join(retErr, err)
		}
	}()
	m.store.ObserveStage(b.Bed, func(step store.StageInStep) { m.updateInitializationStageIn(init, step) })
	if err := trace.stage("stage_in_bedfs", func() error {
		err := m.store.Prepare(ctx, b.Bed)
		trace.source = b.Bed.Status().Store.Source
		return err
	}); err != nil {
		return nil, fmt.Errorf("bed: stage in BedFS %s: %w", b.Name, err)
	}
	staged := m.store.StageResult(b.Bed)
	m.updateInitialization(init, "PreparingBedFS", "preparing BedFS and isolation")
	if err := trace.stage("prepare_bedfs", func() error {
		if err := m.files.Prepare(ctx, b.Bed); err != nil {
			return err
		}
		b.filesystem = m.files.Files(b.Bed)
		return m.privileges.Prepare(ctx, b.Bed)
	}); err != nil {
		return nil, fmt.Errorf("bed: prepare workspace %s: %w", b.Name, err)
	}
	now := time.Now()
	if err := trace.stage("prepare_resident", func() error {
		spec := b.Spec()
		meta, ok := loadMeta(spec.Dir)
		if !ok {
			meta = bedMeta{Version: 1, BedID: b.Name, CreatedAt: now, Sync: spec.Sync}
		}
		meta.Sync = spec.Sync
		if staged.Snapshot != nil {
			meta.SnapshotGeneration = staged.Snapshot.Generation
			meta.SnapshotBytes = staged.Snapshot.Bytes
		}
		if err := saveMeta(spec.Dir, meta); err != nil {
			return err
		}
		model.SpecWriter{}.Update(b.Bed, func(s *model.Spec) { s.CreatedAt = meta.CreatedAt })
		b.lastActiveAt, b.persistedAt, b.generation, b.usage = now, meta.LastPersistedAt, meta.Generation, meta.Usage
		if staged.Restored || b.persistedAt.IsZero() {
			b.persistedAt = now
		}
		if staged.Restored {
			b.usage.LastRestoreMs = staged.RestoreDuration.Milliseconds()
		}
		if m.bedIdleTTL > 0 {
			b.retainUntil = now.Add(m.bedIdleTTL)
		}
		m.store.ObserveLocal(b.Bed, &store.SnapshotInfo{Generation: meta.SnapshotGeneration, Bytes: meta.SnapshotBytes}, filepathx.DirBytes(spec.Dir))
		return nil
	}); err != nil {
		return nil, fmt.Errorf("bed: write meta %s: %w", b.Name, err)
	}
	if m.network.Status().Enabled {
		m.updateInitialization(init, "PreparingNetwork", "preparing Bed network")
		if err := trace.stage("prepare_network", func() error { return m.network.Prepare(ctx, b.Bed) }); err != nil {
			return nil, err
		}
	} else if err := m.network.Prepare(ctx, b.Bed); err != nil {
		return nil, err
	}
	m.updateInitialization(init, "PreparingResources", "preparing Bed accounting group")
	if err := m.resourceManager.Prepare(ctx, b.Bed); err != nil {
		return nil, fmt.Errorf("bed: prepare resources: %w", err)
	}
	if err := m.executorManager.Prepare(ctx, b.Bed); err != nil {
		return nil, err
	}
	if err := m.amenities.Prepare(ctx, b.Bed); err != nil {
		return nil, err
	}
	b.environment = bindEnvironment(m.iso, b.filesystem, m.network.Attachment(b.Bed), m.privileges.User(b.Bed))
	return b, nil
}
