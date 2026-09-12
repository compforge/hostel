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

// Package manager coordinates Hostel Bed lifecycles and admitted operations. A bed is what the control plane calls
// a sandbox: one workspace dir, its own mount namespace (under bwrap), stateful
// shell sessions and one-shot commands running inside it. A pod with one bed ≈
// dedicated; with many beds ≈ shared — each bed still carrying its private
// slice (ns, workspace, shell state, service tenants).
package manager

import (
	model "github.com/qiankunli/hostel/internal/bed"
	"sync"
	"time"

	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"

	"github.com/qiankunli/hostel/internal/bed/store"
)

// ShortID derives a display-only short form of a bed id for log lines. Caller
// ids look like "sandbox-<uuidv7>": the shared prefix carries no information
// (uuidv7 leads with a timestamp) while the entropy sits at the tail, so keep
// the tail. Display only — the full id stays the identity everywhere; the
// "bed resident" line logged at initialization anchors the full↔short mapping, and
// grepping a tail also hits lines that print the full id.
func ShortID(id string) string {
	const tail = 8
	if len(id) <= tail+2 { // "default" and other short ids read best untouched
		return id
	}
	return "…" + id[len(id)-tail:]
}

// Bed coordinates admitted work around the shared Bed model.
type managedBed struct {
	*model.Bed
	local *localIdentity
	// filesystem is the Bed's durable data realm. Executor replacement changes
	// only its process View; bed_home and file identity stay here with the Bed.
	filesystem      *bedfs.FS
	environment     *Environment
	cleanupMu       sync.Mutex // serializes teardown retries for this allocation
	runtimeClosed   bool
	stopSequence    *model.Sequence
	releaseSequence *model.Sequence

	executors *executor.Manager

	mu             sync.Mutex
	persistMu      sync.Mutex // serializes generation bumps and snapshot uploads
	lastActiveAt   time.Time
	retainUntil    time.Time // latest safe eviction time promised to accepted operations
	inflight       int       // bed-scoped operations still in flight
	inflightByKind map[OperationKind]int
	activitySeq    uint64    // changes whenever activity starts or finishes
	generation     int64     // latest local data generation
	persistedAt    time.Time // last successful snapshot (zero = never)
	// Snapshot* describes the durable copy last observed at initialization or
	// persist boundary. LocalBytes is sampled asynchronously by the Store
	// controller; all three are stale-tolerant scheduling hints.
	snapshotGeneration int64
	snapshotBytes      int64
	localBytes         int64
	evicting           bool                // an evict's persist is in flight
	purging            bool                // rejects queued persists once identity deletion owns the bed
	shells             map[string]*Shell   // stateful bash sessions (spec /session)
	sessions           map[string]*Session // revocable stateful holds (session.go)
	usage              Usage               // cumulative; seeded from meta, flushed at persist
	lastInitialization *LifecycleRecord    // bounded diagnostics, never historical
	lastPersist        *LifecycleRecord
}

// Activity is the derived operation activity of a resident Bed. Lifecycle
// location belongs to Phase; evicting and dormant are therefore not activity
// values.
type Activity string

const (
	ActivityActive Activity = "active"
	ActivityIdle   Activity = "idle"
)

// BedStatus is the lifecycle status shared by management detail and inventory.
type BedStatus struct {
	Phase     Phase     `json:"phase"`
	Readiness Readiness `json:"readiness"`
	Activity  Activity  `json:"activity,omitempty"`
}

// Status is one atomic view of a resident bed's scheduler-facing facts.
type Status struct {
	BedStatus
	Generation         int64
	SnapshotGeneration int64
	SnapshotBytes      int64
	LocalBytes         int64
	DataSynced         bool
	Pinned             bool
	LastActiveAt       time.Time
	RetainUntil        time.Time
	Inflight           int
	// Operations breaks Inflight down by kind; Sessions counts open stateful
	// holds by kind (docs/lifecycle.md: sessions never raise Activity).
	Operations map[OperationKind]int
	Sessions   map[SessionKind]int
	Usage      Usage
	Executor   *ExecutorStatus
}

// ExecutorStatus is the current process realm attached to a resident Bed.
// It is ephemeral and never persisted with workspace metadata.
type ExecutorStatus struct {
	ID      string
	Backend string
	State   executor.State
}

// RestoreBytes estimates how much durable data this carrier must download
// before the bed becomes ready. Restore is currently full-snapshot: generation
// is only an equality/freshness token, never a proxy for byte distance.
func (s Status) RestoreBytes() int64 {
	return estimatedRestoreBytes(s.Generation, s.SnapshotGeneration, s.SnapshotBytes, s.DataSynced)
}

func estimatedRestoreBytes(generation, snapshotGeneration, snapshotBytes int64, dataSynced bool) int64 {
	if snapshotBytes <= 0 || dataSynced && generation >= snapshotGeneration {
		return 0
	}
	return snapshotBytes
}

func (b *managedBed) activityLocked() Activity {
	if b.inflight > 0 {
		return ActivityActive
	}
	return ActivityIdle
}

func (b *managedBed) dataSyncedLocked() bool {
	return b.Spec().Sync == store.SyncNoop || !b.lastActiveAt.After(b.persistedAt)
}

// pinnedLocked is a compound capacity fact, not another lifecycle state.
// Callers hold b.mu.
func (b *managedBed) pinnedLocked() bool {
	return b.inflight > 0 || !b.dataSyncedLocked()
}

// Status reports lifecycle, version and deadline from one lock acquisition.
func (b *managedBed) Status() Status {
	b.mu.Lock()
	ops := make(map[OperationKind]int, len(b.inflightByKind))
	for k, n := range b.inflightByKind {
		ops[k] = n
	}
	sessions := make(map[SessionKind]int, 2)
	liveShells := 0
	for _, shell := range b.shells {
		if !shell.Dead() {
			liveShells++
		}
	}
	if n := liveShells; n > 0 {
		sessions[SessionKindShell] = n
	}
	if n := len(b.sessions); n > 0 {
		sessions[SessionKindCDP] = n
	}
	updatedAt := b.lastActiveAt
	if updatedAt.IsZero() {
		updatedAt = b.Spec().CreatedAt
	}
	phase := PhaseResident
	reason := "Initialized"
	if b.evicting {
		phase = PhaseEvicting
		reason = "Evicting"
	}
	status := Status{
		BedStatus: BedStatus{
			Phase:     phase,
			Readiness: Readiness{Ready: true, Reason: reason, UpdatedAt: updatedAt},
			Activity:  b.activityLocked(),
		},
		Generation:         b.generation,
		SnapshotGeneration: b.snapshotGeneration,
		SnapshotBytes:      b.snapshotBytes,
		LocalBytes:         b.localBytes,
		DataSynced:         b.dataSyncedLocked(),
		Pinned:             b.pinnedLocked(),
		LastActiveAt:       b.lastActiveAt,
		RetainUntil:        b.retainUntil,
		Inflight:           b.inflight,
		Operations:         ops,
		Sessions:           sessions,
		Usage:              b.usage,
	}
	currentExecutor := b.executors.Current(b.Bed)
	b.mu.Unlock()
	if currentExecutor != nil {
		status.Executor = &ExecutorStatus{
			ID: currentExecutor.ID(), Backend: currentExecutor.Backend(), State: currentExecutor.State(),
		}
	}
	return status
}

// Activity reports whether this resident Bed currently holds an operation.
func (b *managedBed) Activity() Activity { return b.Status().Activity }

// Short is ShortID(b.ID) — the log-friendly form of this bed's id.
func (b *managedBed) Short() string { return ShortID(b.ID) }

// touchLocked refreshes the activity watermarks; the caller holds b.mu.
func (b *managedBed) touchLocked(now time.Time, idleTTL time.Duration) {
	b.lastActiveAt = now
	b.activitySeq++
	if idleTTL > 0 {
		if retainUntil := now.Add(idleTTL); retainUntil.After(b.retainUntil) {
			b.retainUntil = retainUntil
		}
	}
}

// LastActiveAt reports the most recent request or command activity.
func (b *managedBed) LastActiveAt() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastActiveAt
}

// RetainUntil is the latest safe eviction time promised to accepted work.
func (b *managedBed) RetainUntil() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.retainUntil
}

// Inflight reports bed-scoped operations still in flight.
func (b *managedBed) Inflight() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inflight
}

// BedFS returns the filesystem owned by this Bed.
func (b *managedBed) BedFS() *bedfs.FS { return b.filesystem }

// Home is the carrier path of bed_home. It is derived from BedFS rather than
// reconstructed from the workspace-root path convention.
func (b *managedBed) Home() string { return b.filesystem.Home() }

// Workspace is the carrier path of the Bed's default workspace.
func (b *managedBed) Workspace() string { return b.filesystem.Workspace() }

// RecordCommand adds one finished run (foreground, session or background) to
// the bed's usage counters. Failed runs count too — they are load all the same.
func (b *managedBed) RecordCommand(d time.Duration) {
	b.mu.Lock()
	b.usage.CmdCount++
	b.usage.CmdTotalMs += d.Milliseconds()
	b.mu.Unlock()
}

// Usage returns a copy of the bed's current usage counters.
func (b *managedBed) Usage() Usage {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usage
}

// Resident is a Bed Manager operation handle. Runtime coordination is distinct
// from the embedded Bed model shared with domains.
type Resident = managedBed
