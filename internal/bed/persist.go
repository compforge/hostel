// Copyright 2026 Li Qiankun
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bed

import (
	"context"
	"fmt"
	"time"
)

// persistBed snapshots the bed dir (portable meta + data) and, on success,
// advances both the in-memory and on-disk persistence watermarks.
//
// Ordering constraint: the generation bump is saved BEFORE packing (the
// snapshot must carry its own generation), but LastPersistedAt only AFTER a
// successful upload — a failed upload leaving the local generation ahead is
// accurate ("locally dirty"), while a falsely-advanced LastPersistedAt would
// make restart-time dirty tracking skip data that never reached the store.
func (m *Manager) persistBed(ctx context.Context, b *Bed, trigger string) (retErr error) {
	trace := beginLifecycle(b.ID, lifecyclePersist)
	trace.trigger = trigger
	defer func() {
		b.recordLifecycle(trace.finish(lifecycleResult(retErr), retErr))
	}()

	if err := trace.stage("wait_persist_lock", func() error {
		b.persistMu.Lock()
		return nil
	}); err != nil {
		return err
	}
	defer b.persistMu.Unlock()

	var meta bedMeta
	if err := trace.stage("prepare_snapshot", func() error {
		var ok bool
		meta, ok = loadMeta(b.Dir)
		if !ok {
			meta = bedMeta{Version: 1, BedID: b.ID, CreatedAt: b.CreatedAt}
		}
		meta.Generation++
		// Flush counters before packing so they travel with the snapshot.
		// LastPersistMs necessarily lags one persist behind because this
		// upload's duration is not known until after packing.
		meta.Usage = b.Usage()
		if err := saveMeta(b.Dir, meta); err != nil {
			return err
		}
		b.mu.Lock()
		b.generation = meta.Generation
		b.mu.Unlock()
		return nil
	}); err != nil {
		return fmt.Errorf("bed: bump generation %s: %w", b.ID, err)
	}

	var persistedAt time.Time
	var persistStarted time.Time
	if err := trace.stage("persist_store", func() error {
		persistStarted = time.Now()
		if err := m.store.Persist(ctx, b.ID, b.Dir, meta.Generation); err != nil {
			return err
		}
		persistedAt = time.Now()
		return nil
	}); err != nil {
		return err
	}

	_ = trace.stage("commit_watermark", func() error {
		b.mu.Lock()
		b.persistedAt = persistedAt
		b.usage.LastPersistMs = persistedAt.Sub(persistStarted).Milliseconds()
		meta.Usage = b.usage
		b.mu.Unlock()
		meta.LastPersistedAt = persistedAt
		_ = saveMeta(b.Dir, meta) // best-effort; in-memory watermark is set
		return nil
	})
	return nil
}

// Checkpoint snapshots a bed's workspace now, without tearing it down.
func (m *Manager) Checkpoint(ctx context.Context, id string) error {
	b, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("bed: unknown bed %q", id)
	}
	finish, err := m.BeginOperation(b, OpCheckpoint, 0)
	if err != nil {
		return err
	}
	defer finish()
	return m.persistBed(ctx, b, "checkpoint")
}

// PersistDirty is the periodic safety net: snapshot every bed touched since
// its last snapshot. Best-effort — a failed bed is retried next tick. Returns
// ids persisted. The default bed is included (its data matters most).
func (m *Manager) PersistDirty(ctx context.Context) []string {
	var done []string
	for _, b := range m.List() {
		b.mu.Lock()
		dirty := b.lastActiveAt.After(b.persistedAt)
		b.mu.Unlock()
		if !dirty {
			continue
		}
		if err := m.persistBed(ctx, b, "periodic"); err != nil {
			continue
		}
		done = append(done, b.ID)
	}
	return done
}
