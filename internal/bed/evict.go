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

// Evict releases a bed's compute while keeping its identity (IDLE →
// EVICTING → LUGGAGE, docs/persistence.md §4): persist, then tear down and
// free the max-beds slot. The local dir stays behind as luggage — a warm
// cache of the DORMANT bed, so a same-instance resume skips the snapshot
// download; luggage GC reclaims disk separately. Returns evicted=false
// without error when the eviction was CANCELED because the bed saw new
// activity during the persist window — serving beats reclaiming, and
// removing runtime state after a mid-persist write would silently drop that
// write. A persist failure aborts the evict (never destroy the only copy).
func (m *Manager) Evict(id string) (bool, error) {
	return m.evict(id, nil)
}

// evictExpired is the idle-GC path. Unlike explicit Evict, it atomically
// re-checks the bed's deadline and running exec count before entering EVICTING.
func (m *Manager) evictExpired(id string, now time.Time) (bool, error) {
	return m.evict(id, &now)
}

func (m *Manager) evict(id string, expiryCutoff *time.Time) (evicted bool, retErr error) {
	if id == "" {
		id = m.defaultBed
	}
	m.mu.Lock()
	b, ok := m.beds[id]
	m.mu.Unlock()
	if !ok {
		return false, nil // not resident; nothing to evict
	}
	trace := beginLifecycle(id, lifecycleEvict)
	defer func() {
		result := lifecycleResult(retErr)
		if retErr == nil && !evicted {
			result = lifecycleCanceled
		}
		trace.finish(result, retErr)
	}()

	// Enter EVICTING: remember the activity watermark we snapshot against.
	b.mu.Lock()
	if b.evicting {
		b.mu.Unlock()
		return false, nil // another evict is already in flight
	}
	if b.inflight > 0 || (expiryCutoff != nil && (b.retainUntil.IsZero() || b.retainUntil.After(*expiryCutoff))) {
		b.mu.Unlock()
		return false, nil
	}
	b.evicting = true
	activitySeq := b.activitySeq
	watermark := b.lastActiveAt
	b.mu.Unlock()

	// Revoke BEFORE persist (docs/lifecycle.md): stateful sessions cannot be
	// waited out, so evict actively ends them — and their writes must not race
	// the snapshot. The wait is bounded, so a stubborn handler stalls the
	// evict at most sessionRevokeWait.
	_ = trace.stage("revoke_sessions", func() error {
		m.revokeSessions(b)
		return nil
	})

	if err := m.persistBed(context.Background(), b, "evict"); err != nil {
		b.mu.Lock()
		b.evicting = false
		b.mu.Unlock()
		return false, fmt.Errorf("bed: persist before evict %s: %w", id, err)
	}

	// Atomic re-check: activity during the persist window cancels the evict.
	// The snapshot we just took is still valid (it's simply not the final
	// word), so nothing is wasted.
	// Commit removal under the same lock order as BeginOperation. Either the
	// operation is admitted first and changes activitySeq/inflight, or this
	// delete wins and later admission observes a stale Bed pointer.
	m.mu.Lock()
	b.mu.Lock()
	current, present := m.beds[id]
	if !present || current != b || b.activitySeq != activitySeq || b.inflight > 0 {
		b.evicting = false
		b.mu.Unlock()
		m.mu.Unlock()
		return false, nil
	}
	delete(m.beds, id)
	m.residentBeds.Add(-1)
	b.mu.Unlock()
	m.mu.Unlock()
	m.teardown(b)
	// Stamp the luggage with its last activity so luggage GC can order cold
	// copies by recency with no in-memory state. Best-effort — a missing
	// stamp only weakens GC ordering, never correctness.
	if meta, ok := loadMeta(b.Dir); ok {
		meta.LastActiveAt = watermark
		_ = saveMeta(b.Dir, meta)
	}
	b.mu.Lock()
	b.evicting = false
	b.mu.Unlock()
	return true, nil
}

// ErrPurgeDefault marks a client mistake (4xx), not a server failure: the
// default bed is the single-tenant fallback and cannot be purged.
var ErrPurgeDefault = errors.New("bed: refusing to purge the default bed")

// Purge ends a bed's identity: tear down (no persist), remove the local dir
// (active workspace or leftover luggage), and delete the snapshot. Explicitly
// destructive — the caller asked for the data to be gone, so concurrent
// activity does not cancel it.
func (m *Manager) Purge(id string) error {
	if id == "" || id == m.defaultBed {
		return ErrPurgeDefault
	}
	// Purge touches the filesystem even for beds not in memory (luggage), so
	// the id must be validated here too — never path-join an unchecked id.
	if err := validBedID(id); err != nil {
		return err
	}
	m.mu.Lock()
	b, ok := m.beds[id]
	if ok {
		delete(m.beds, id)
		m.residentBeds.Add(-1)
	}
	m.mu.Unlock()
	if ok {
		m.teardown(b)
	}
	// DORMANT beds may leave luggage (same path as an active bed's dir).
	if err := os.RemoveAll(filepath.Join(m.root, id)); err != nil {
		return err
	}
	// DORMANT (or never-existed) beds still have a snapshot to remove.
	return m.store.Delete(context.Background(), id)
}

// teardown kills a bed's runtime state: shells, one-shot commands, service
// tenants. The workspace is untouched — callers decide its fate. On the
// evict path the shells are already closed (revoke stage, before persist);
// the close here covers Purge, which has no revoke.
func (m *Manager) teardown(b *Bed) {
	b.mu.Lock()
	for sid, sh := range b.shells {
		sh.Close()
		delete(b.shells, sid)
	}
	b.mu.Unlock()
	m.commands.killBed(b.ID)
	// The spawner sweep is the authoritative kill: it also catches processes
	// the registry never saw (foreground RunForeground runs are unregistered).
	m.spawner.KillBed(b.ID)
	_ = m.resources.Release(b.ID)
	m.amenities.ReleaseAll(b.ID)
}

// CollectExpired reaps beds whose promised expiry has elapsed. The final
// expiry/running-exec check happens again under the bed lock in evictExpired,
// closing the scan→evict race with a concurrent command.
// The default bed is never reaped.
func (m *Manager) CollectExpired(now time.Time) []string {
	var stale []string
	m.mu.Lock()
	for id, b := range m.beds {
		if id == m.defaultBed {
			continue
		}
		retainUntil := b.RetainUntil()
		if !retainUntil.IsZero() && !retainUntil.After(now) {
			stale = append(stale, id)
		}
	}
	m.mu.Unlock()
	var reaped []string
	for _, id := range stale {
		if ok, _ := m.evictExpired(id, now); ok {
			reaped = append(reaped, id)
		}
	}
	return reaped
}
