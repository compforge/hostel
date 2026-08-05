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

// Package bed is hostel's isolation unit. A bed is what the control plane calls
// a sandbox: one workspace dir, its own mount namespace (under bwrap), stateful
// shell sessions and one-shot commands running inside it. A pod with one bed ≈
// dedicated; with many beds ≈ shared — each bed still carrying its private
// slice (ns, workspace, shell state, service tenants).
package bed

import (
	"sync"
	"time"

	"github.com/qiankunli/hostel/internal/fsops"
)

// ShortID derives a display-only short form of a bed id for log lines. Caller
// ids look like "sandbox-<uuidv7>": the shared prefix carries no information
// (uuidv7 leads with a timestamp) while the entropy sits at the tail, so keep
// the tail. Display only — the full id stays the identity everywhere; the
// "bed resident" line logged at activation anchors the full↔short mapping, and
// grepping a tail also hits lines that print the full id.
func ShortID(id string) string {
	const tail = 8
	if len(id) <= tail+2 { // "default" and other short ids read best untouched
		return id
	}
	return "…" + id[len(id)-tail:]
}

// Bed is one isolation unit.
type Bed struct {
	ID string
	// Dir is the bed's dir: meta.json + data/ (docs/persistence.md §4).
	// Snapshots pack this dir; bed code never sees it.
	Dir string
	// Home is Dir/data — the bed_home: the client's "/" and all the
	// bed may touch. Everything a client path names lands below it.
	Home string
	// Workspace is Home/workspace — the OpenSandbox workspace: canonical
	// /workspace bind source under suite, default cwd, browser artifact home.
	Workspace string
	CreatedAt time.Time // survives evict/resume via snapshot meta

	// paths converts between this bed's three path spaces (client / host /
	// in-bed). THE place for any path stitching — callers must not rebuild
	// MountPoint()+Rel+Join by hand (that's how the exec-cwd ENOENT happened).
	paths fsops.Paths

	mu             sync.Mutex
	persistMu      sync.Mutex // serializes generation bumps and snapshot uploads
	lastActiveAt   time.Time
	retainUntil    time.Time // latest safe eviction time promised to accepted operations
	inflight       int       // bed-scoped operations still in flight
	inflightByKind map[OperationKind]int
	activitySeq    uint64              // changes whenever activity starts or finishes
	generation     int64               // latest local data generation
	persistedAt    time.Time           // last successful snapshot (zero = never)
	evicting       bool                // an evict's persist is in flight
	shells         map[string]*Shell   // stateful bash sessions (spec /session)
	sessions       map[string]*Session // revocable stateful holds (session.go)
	usage          Usage               // cumulative; seeded from meta, flushed at persist
	lastActivation *LifecycleRecord    // bounded diagnostics, never historical
	lastPersist    *LifecycleRecord
}

// State is the mutually-exclusive operational state reported to the scheduler.
// Data freshness (Generation) and retention (RetainUntil) are separate dimensions.
type State string

const (
	StateActive   State = "active"
	StateIdle     State = "idle"
	StateEvicting State = "evicting"
	StateDormant  State = "dormant"
)

// Status is one atomic view of a resident bed's scheduler-facing facts.
type Status struct {
	State        State
	Generation   int64
	LastActiveAt time.Time
	RetainUntil  time.Time
	Inflight     int
	// Operations breaks Inflight down by kind; Sessions counts open stateful
	// holds by kind (docs/lifecycle.md: sessions never raise State).
	Operations map[OperationKind]int
	Sessions   map[SessionKind]int
	Usage      Usage
}

func (b *Bed) stateLocked() State {
	if b.inflight > 0 {
		return StateActive
	}
	if b.evicting {
		return StateEvicting
	}
	return StateIdle
}

// Status reports lifecycle, version and deadline from one lock acquisition.
func (b *Bed) Status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	ops := make(map[OperationKind]int, len(b.inflightByKind))
	for k, n := range b.inflightByKind {
		ops[k] = n
	}
	sessions := make(map[SessionKind]int, 2)
	if n := len(b.shells); n > 0 {
		sessions[SessionKindShell] = n
	}
	if n := len(b.sessions); n > 0 {
		sessions[SessionKindCDP] = n
	}
	return Status{
		State:        b.stateLocked(),
		Generation:   b.generation,
		LastActiveAt: b.lastActiveAt,
		RetainUntil:  b.retainUntil,
		Inflight:     b.inflight,
		Operations:   ops,
		Sessions:     sessions,
		Usage:        b.usage,
	}
}

// State reports the current operational state.
func (b *Bed) State() State { return b.Status().State }

// Short is ShortID(b.ID) — the log-friendly form of this bed's id.
func (b *Bed) Short() string { return ShortID(b.ID) }

func (b *Bed) touch(idleTTL time.Duration) {
	now := time.Now()
	b.mu.Lock()
	b.touchLocked(now, idleTTL)
	b.mu.Unlock()
}

// touchLocked refreshes the activity watermarks; the caller holds b.mu.
func (b *Bed) touchLocked(now time.Time, idleTTL time.Duration) {
	b.lastActiveAt = now
	b.activitySeq++
	if idleTTL > 0 {
		if retainUntil := now.Add(idleTTL); retainUntil.After(b.retainUntil) {
			b.retainUntil = retainUntil
		}
	}
}

// LastActiveAt reports the most recent request or command activity.
func (b *Bed) LastActiveAt() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastActiveAt
}

// RetainUntil is the latest safe eviction time promised to accepted work.
func (b *Bed) RetainUntil() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.retainUntil
}

// Inflight reports bed-scoped operations still in flight.
func (b *Bed) Inflight() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inflight
}

// Paths converts between this bed's path spaces (client / host / in-bed).
// Immutable value set at creation; safe without the lock.
func (b *Bed) Paths() fsops.Paths { return b.paths }

// RecordCommand adds one finished run (foreground, session or background) to
// the bed's usage counters. Failed runs count too — they are load all the same.
func (b *Bed) RecordCommand(d time.Duration) {
	b.mu.Lock()
	b.usage.CmdCount++
	b.usage.CmdTotalMs += d.Milliseconds()
	b.mu.Unlock()
}

// Usage returns a copy of the bed's current usage counters.
func (b *Bed) Usage() Usage {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usage
}
