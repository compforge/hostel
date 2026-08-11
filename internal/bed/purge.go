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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrPurgeDefault marks a client mistake (4xx), not a server failure: the
// default bed is the single-tenant fallback and cannot be purged.
var ErrPurgeDefault = errors.New("bed: refusing to purge the default bed")

// ErrBedPurging prevents a create from acquiring the same Bed identity while
// its local and durable data are still being deleted.
var ErrBedPurging = errors.New("bed: identity is being purged")

const purgeStoreTimeout = 30 * time.Second

type bedPurge struct {
	status InitializationStatus
	done   chan struct{}
	err    error
}

// Purge ends a bed's identity: tear down (no persist), remove the local dir
// (active workspace or leftover luggage), and delete the snapshot. A per-ID
// purge fence remains visible for the whole transition, so no concurrent
// InitializeBed can recreate the identity before deletion commits.
func (m *Manager) Purge(ctx context.Context, id string) error {
	if id == "" || id == m.defaultBed {
		return ErrPurgeDefault
	}
	if err := validBedID(id); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	purge, owner := m.beginPurge(id)
	if !owner {
		waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), purgeStoreTimeout)
		defer cancel()
		select {
		case <-purge.done:
			return purge.err
		case <-waitCtx.Done():
			return fmt.Errorf("bed: wait for purge %s: %w", id, waitCtx.Err())
		}
	}

	err := m.purgeOwned(ctx, id)
	m.finishPurge(id, purge, err)
	return err
}

func (m *Manager) beginPurge(id string) (*bedPurge, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.purges[id]; ok {
		return current, false
	}
	now := time.Now()
	purge := &bedPurge{
		status: InitializationStatus{
			ID: id,
			BedStatus: BedStatus{
				Phase:     PhasePurging,
				Readiness: Readiness{Reason: "Purging", UpdatedAt: now},
			},
			StartedAt: now,
		},
		done: make(chan struct{}),
	}
	m.purges[id] = purge
	return purge, true
}

func (m *Manager) finishPurge(id string, purge *bedPurge, err error) {
	m.mu.Lock()
	purge.err = err
	if current, ok := m.purges[id]; ok && current == purge {
		delete(m.purges, id)
	}
	m.mu.Unlock()
	close(purge.done)
}

func (m *Manager) purgeOwned(ctx context.Context, id string) error {
	joinCtx, cancelJoin := context.WithTimeout(context.WithoutCancel(ctx), purgeStoreTimeout)
	_, err := m.cancelInitialization(joinCtx, id)
	cancelJoin()
	if err != nil {
		return fmt.Errorf("bed: stop initialization before purge %s: %w", id, err)
	}

	m.mu.Lock()
	b, ok := m.beds[id]
	if ok {
		b.mu.Lock()
		b.purging = true
		delete(m.beds, id)
		m.residentBeds.Add(-1)
		if b.pinnedLocked() {
			m.pinnedBeds.Add(-1)
		}
		b.mu.Unlock()
	}
	m.mu.Unlock()
	if ok {
		// A persist that already passed admission may still be uploading. Join it
		// before Delete; queued persists observe purging after this lock is released
		// and fail instead of recreating the snapshot.
		b.persistMu.Lock()
		defer b.persistMu.Unlock()
		m.teardown(b)
	}
	if err := os.RemoveAll(filepath.Join(m.root, id)); err != nil {
		return err
	}
	deleteCtx, cancelDelete := context.WithTimeout(context.WithoutCancel(ctx), purgeStoreTimeout)
	defer cancelDelete()
	return m.store.Delete(deleteCtx, id)
}
