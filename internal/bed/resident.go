package bed

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/qiankunli/go-stdx/filepathx"
	"github.com/qiankunli/hostel/internal/bedfs"
	"github.com/qiankunli/hostel/internal/isolation"
	"github.com/qiankunli/hostel/internal/network"
	"github.com/qiankunli/hostel/internal/store"
)

// initializeResidentBed performs the slow, private part of Bed
// initialization. Admission and single-flight ownership are established by
// InitializeBed before this function runs; it must not hold m.mu while doing
// Store or filesystem I/O.
func (m *Manager) initializeResidentBed(ctx context.Context, initialization *bedInitialization) (resolved *Bed, retErr error) {
	id := initialization.status.ID
	trace := beginLifecycle(ctx, id, lifecycleInitialize)
	defer func() {
		record := trace.finish(lifecycleResult(retErr), retErr)
		if resolved != nil {
			resolved.recordLifecycle(record)
		}
	}()
	bedDir := filepath.Join(m.root, id)
	dataDir := filepath.Join(bedDir, "data")
	// Recovery: an orphaned local directory from an unclean shutdown or older
	// version may be reused when its generation is at least as new as the
	// snapshot. A stale copy is replaced atomically, never merged. A restore
	// failure leaves the orphan untouched and fails initialization rather than
	// silently starting empty.
	kind := initialization.status.Store
	local, localPresent := loadMeta(bedDir)
	var staged store.StageInResult
	if err := trace.stage("stage_in_bedfs", func() error {
		var err error
		staged, err = m.store.StageInBedFS(ctx, kind, store.StageInRequest{
			BedID:           id,
			BedDir:          bedDir,
			LocalPresent:    localPresent,
			LocalGeneration: local.Generation,
			OnStep: func(step store.StageInStep) {
				m.updateInitializationStageIn(initialization, step)
			},
		})
		trace.source = string(staged.Source)
		return err
	}); err != nil {
		return nil, fmt.Errorf("bed: stage in BedFS %s: %w", id, err)
	}
	wsDir := filepath.Join(dataDir, "workspace")
	m.updateInitialization(initialization, "PreparingBedFS", "preparing BedFS and isolation")
	var filesystem *bedfs.FS
	if err := trace.stage("prepare_bedfs", func() error {
		if err := os.MkdirAll(wsDir, 0o755); err != nil {
			return err
		}
		var err error
		filesystem, err = bedfs.New(dataDir)
		if err != nil {
			return err
		}
		// Prepare after restore repopulates the tree and before the bed serves.
		if p, ok := m.iso.(isolation.Preparer); ok {
			return p.Prepare(filesystem)
		}
		return nil
	}); err != nil {
		if filesystem != nil {
			_ = filesystem.Close()
		}
		return nil, fmt.Errorf("bed: prepare workspace %s: %w", id, err)
	}

	now := time.Now()
	var meta bedMeta
	var b *Bed
	if err := trace.stage("prepare_resident", func() error {
		var ok bool
		meta, ok = loadMeta(bedDir)
		if !ok {
			meta = bedMeta{Version: 1, BedID: id, CreatedAt: now, Store: kind}
			if err := saveMeta(bedDir, meta); err != nil {
				return err
			}
		}
		if meta.Store != kind {
			meta.Store = kind
			if err := saveMeta(bedDir, meta); err != nil {
				return err
			}
		}
		// Dirty-tracking baseline: a just-restored bed is in sync NOW; a dir
		// that survived a process restart trusts its on-disk timestamp.
		persistedAt := meta.LastPersistedAt
		if staged.Restored || persistedAt.IsZero() {
			persistedAt = now
		}
		usage := meta.Usage
		if staged.Restored {
			usage.LastRestoreMs = staged.RestoreDuration.Milliseconds()
		}
		retainUntil := time.Time{}
		if m.bedIdleTTL > 0 {
			retainUntil = now.Add(m.bedIdleTTL)
		}
		b = &Bed{
			ID: id, Dir: bedDir,
			CreatedAt: meta.CreatedAt, lastActiveAt: now, retainUntil: retainUntil,
			generation: meta.Generation, persistedAt: persistedAt, usage: usage,
			snapshotGeneration: meta.SnapshotGeneration,
			snapshotBytes:      meta.SnapshotBytes,
			localBytes:         filepathx.DirBytes(bedDir),
			Store:              kind,
			shells:             make(map[string]*Shell),
			sessions:           make(map[string]*Session),
			inflightByKind:     make(map[OperationKind]int),
			filesystem:         filesystem,
		}
		resolved = b
		if staged.Snapshot != nil {
			b.snapshotGeneration = staged.Snapshot.Generation
			b.snapshotBytes = staged.Snapshot.Bytes
			meta.SnapshotGeneration = staged.Snapshot.Generation
			meta.SnapshotBytes = staged.Snapshot.Bytes
			_ = saveMeta(bedDir, meta)
		}
		return nil
	}); err != nil {
		_ = filesystem.Close()
		return nil, fmt.Errorf("bed: write meta %s: %w", id, err)
	}
	var attachment network.Attachment
	if m.network.Report().Enabled {
		m.updateInitialization(initialization, "PreparingNetwork", "preparing Bed network")
		if err := trace.stage("prepare_network", func() error {
			var err error
			attachment, err = m.network.Acquire(ctx, id)
			return err
		}); err != nil {
			_ = filesystem.Close()
			return nil, err
		}
	}

	environment := isolation.Bind(m.iso, filesystem, attachment)
	b.environment = environment
	// Prepare the accounting parent with the Bed; Executor children are lazy.
	m.updateInitialization(initialization, "PreparingResources", "preparing Bed accounting group")
	group, err := m.resources.OpenGroup(id)
	if err == nil && group != nil {
		err = group.Close()
	}
	if err != nil {
		m.mu.Lock()
		m.retirements[id] = b
		m.mu.Unlock()
		return nil, fmt.Errorf("bed: prepare resources: %w", err)
	}

	return b, nil
}
