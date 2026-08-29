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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/qiankunli/go-stdx/filepathx"
)

// Luggage is a DORMANT bed's local dir left behind by Evict: a warm cache of
// the snapshot, so a same-instance resume skips the download. It is never the
// authoritative copy (the snapshot is — docs/store.md); deleting
// luggage costs at most one extra Restore. The exception is the noop store,
// where nothing else exists: there luggage GC is destruction, the price of
// the "nothing persists" world.

// gcTmpPrefix marks a luggage dir claimed by GC: renamed under the manager
// lock (atomic — a concurrent Ensure either sees the bed dir or doesn't,
// never a half-deleted one), then removed outside it. Leftovers from a crash
// are swept on the next tick. It can never collide with a bed id (validBedID
// rejects a leading dot).
const gcTmpPrefix = ".gc-"

// LuggageEntry describes one cold local copy for GC and inventory reporting.
type LuggageEntry struct {
	BedID string
	// Bytes is the dir's file size total — the disk this entry occupies.
	Bytes int64
	// Generation is the local copy's persist counter (0 when meta is missing).
	Generation int64
	// Snapshot* is the last durable Stat result cached in meta.json.
	SnapshotGeneration int64
	SnapshotBytes      int64
	// LastActiveAt orders LRU eviction: the evict-time stamp, falling back to
	// LastPersistedAt and then dir mtime for copies predating the stamp.
	LastActiveAt time.Time
	// Usage is the activity picture the bed left behind (from its meta).
	Usage Usage
}

// ListLuggage scans the workspace root for bed dirs that are not resident —
// the local copies of DORMANT beds. The default bed is never luggage (its
// dir is permanent by contract).
func (m *Manager) ListLuggage() []LuggageEntry {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil
	}
	m.mu.Lock()
	active := make(map[string]bool, len(m.beds)+len(m.initializations)+len(m.purges))
	for id := range m.beds {
		active[id] = true
	}
	for id := range m.initializations {
		active[id] = true
	}
	for id := range m.purges {
		active[id] = true
	}
	m.mu.Unlock()

	var out []LuggageEntry
	for _, e := range entries {
		id := e.Name()
		if !e.IsDir() || active[id] || id == m.defaultBed || validBedID(id) != nil {
			continue
		}
		dir := filepath.Join(m.root, id)
		l := LuggageEntry{BedID: id, Bytes: filepathx.DirBytes(dir)}
		if meta, ok := loadMeta(dir); ok {
			l.Generation = meta.Generation
			l.SnapshotGeneration = meta.SnapshotGeneration
			l.SnapshotBytes = meta.SnapshotBytes
			l.LastActiveAt = meta.LastActiveAt
			l.Usage = meta.Usage
			if l.LastActiveAt.IsZero() {
				l.LastActiveAt = meta.LastPersistedAt
			}
		}
		if l.LastActiveAt.IsZero() {
			if fi, err := e.Info(); err == nil {
				l.LastActiveAt = fi.ModTime()
			}
		}
		out = append(out, l)
	}
	return out
}

// SetLuggageLimits configures the disk watermarks (bytes; high 0 = GC off).
// Call once at startup, before serving — the fields are not synchronized.
func (m *Manager) SetLuggageLimits(high, low int64) {
	m.luggageHigh, m.luggageLow = high, low
}

// LuggageLimits reports the configured watermarks for healthz/inventory.
func (m *Manager) LuggageLimits() (high, low int64) {
	return m.luggageHigh, m.luggageLow
}

// CollectLuggage enforces the luggage disk watermarks: when the total exceeds
// the high watermark, delete cold copies until under the low one. Returns
// reaped ids. Deletion order is the cost-aware eviction seam (v1: score =
// recency): stale-generation copies first — the snapshot is newer, so they
// are pure garbage — then least recently used.
func (m *Manager) CollectLuggage(ctx context.Context) []string {
	if m.luggageHigh <= 0 {
		return nil
	}
	m.sweepGCLeftovers()
	luggage := m.ListLuggage()
	var total int64
	for _, l := range luggage {
		total += l.Bytes
	}
	if total <= m.luggageHigh {
		return nil
	}
	// One Stat (HEAD) per entry, paid only on the over-watermark path.
	stale := map[string]bool{}
	for _, l := range luggage {
		if info, err := m.store.Stat(ctx, l.BedID); err == nil && info != nil && l.Generation < info.Generation {
			stale[l.BedID] = true
		}
	}
	sort.Slice(luggage, func(i, j int) bool {
		if stale[luggage[i].BedID] != stale[luggage[j].BedID] {
			return stale[luggage[i].BedID]
		}
		return luggage[i].LastActiveAt.Before(luggage[j].LastActiveAt)
	})
	var reaped []string
	for _, l := range luggage {
		if total <= m.luggageLow {
			break
		}
		if m.removeLuggage(l.BedID) {
			total -= l.Bytes
			reaped = append(reaped, l.BedID)
		}
	}
	return reaped
}

// removeLuggage deletes one cold copy. The rename happens under the manager
// lock so it is atomic against Ensure: a bed resurrected since the scan is
// skipped, and an Ensure arriving after the rename sees no dir and takes the
// cold-restore path — never a half-deleted copy.
func (m *Manager) removeLuggage(id string) bool {
	dir := filepath.Join(m.root, id)
	tmp := filepath.Join(m.root, gcTmpPrefix+id)
	m.mu.Lock()
	if _, ok := m.beds[id]; ok {
		m.mu.Unlock()
		return false
	}
	if _, ok := m.initializations[id]; ok {
		m.mu.Unlock()
		return false
	}
	if _, ok := m.purges[id]; ok {
		m.mu.Unlock()
		return false
	}
	if err := os.Rename(dir, tmp); err != nil {
		m.mu.Unlock()
		return false
	}
	m.mu.Unlock()
	_ = os.RemoveAll(tmp)
	return true
}

// sweepGCLeftovers removes rename-then-crash debris from earlier GC runs.
func (m *Manager) sweepGCLeftovers() {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), gcTmpPrefix) {
			_ = os.RemoveAll(filepath.Join(m.root, e.Name()))
		}
	}
}

// InventoryBed is one row of the scheduler-facing inventory: every bed this
// instance holds, in any lifecycle phase or as luggage. Generation is
// the last PERSISTED counter — an active bed's workspace may be ahead of it,
// which is exactly what "the authoritative copy is here" means.
type InventoryBed struct {
	ID                 string    `json:"id"`
	Status             BedStatus `json:"status"`
	Generation         int64     `json:"generation"`
	SnapshotGeneration int64     `json:"snapshot_generation,omitempty"`
	SnapshotBytes      int64     `json:"snapshot_bytes,omitempty"`
	LocalBytes         int64     `json:"local_bytes,omitempty"`
	RestoreBytes       int64     `json:"restore_bytes,omitempty"`
	DataSynced         bool      `json:"data_synced"`
	Pinned             bool      `json:"pinned"`
	LastActiveAt       time.Time `json:"last_active_at"`
	RetainUntil        time.Time `json:"retained_until,omitzero"` // resident beds only
	// Usage lets the scheduler weigh placement and migration: command
	// rate/duration derive from deltas between polls; Last{Persist,Restore}Ms
	// approximate this bed's migration cost (node-specific — see Usage).
	Usage Usage `json:"usage,omitzero"`
}

// Inventory reports all local beds for the upstream scheduler: placement
// wants "who has a fresh copy" (generation) and "who is loaded" (active
// count). The result is a stale-tolerant hint — freshness is re-checked at
// initialization (Ensure), so a scheduler routing on outdated data is slow,
// never wrong.
//
// +spec=`Scheduler inventory contains tenant beds only; the compatibility default bed never participates in placement or capacity projections.`
// +case:id=default_bed_inventory,desc=`Use the default bed and then query scheduler inventory`,expect=`the default bed is absent and tenant capacity remains available`
func (m *Manager) Inventory() []InventoryBed {
	beds := m.List()
	out := make([]InventoryBed, 0, len(beds))
	for _, b := range beds {
		if b.ID == m.defaultBed {
			continue
		}
		status := b.Status()
		entry := InventoryBed{
			ID:                 b.ID,
			Status:             status.BedStatus,
			Generation:         status.Generation,
			SnapshotGeneration: status.SnapshotGeneration,
			SnapshotBytes:      status.SnapshotBytes,
			LocalBytes:         status.LocalBytes,
			RestoreBytes:       status.RestoreBytes(),
			DataSynced:         status.DataSynced,
			Pinned:             status.Pinned,
			LastActiveAt:       status.LastActiveAt,
			RetainUntil:        status.RetainUntil,
			Usage:              status.Usage,
		}
		out = append(out, entry)
	}
	for _, initialization := range m.initializationStatuses() {
		if initialization.ID == m.defaultBed {
			continue
		}
		out = append(out, InventoryBed{
			ID:           initialization.ID,
			Status:       initialization.BedStatus,
			LastActiveAt: initialization.StartedAt,
		})
	}
	for _, l := range m.ListLuggage() {
		out = append(out, InventoryBed{
			ID: l.BedID,
			Status: BedStatus{
				Phase:     PhaseDormant,
				Readiness: Readiness{Reason: "NotResident", UpdatedAt: l.LastActiveAt},
			},
			Generation:         l.Generation,
			SnapshotGeneration: l.SnapshotGeneration,
			SnapshotBytes:      l.SnapshotBytes,
			LocalBytes:         l.Bytes,
			RestoreBytes:       estimatedRestoreBytes(l.Generation, l.SnapshotGeneration, l.SnapshotBytes, true),
			DataSynced:         true,
			LastActiveAt:       l.LastActiveAt,
			Usage:              l.Usage,
		})
	}
	return out
}
