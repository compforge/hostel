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
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qiankunli/hostel/internal/amenity"
	"github.com/qiankunli/hostel/internal/fsops"
	"github.com/qiankunli/hostel/internal/isolation"
	"github.com/qiankunli/hostel/internal/resource"
	"github.com/qiankunli/hostel/internal/store"
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

// Manager owns the set of beds and their lifecycle. Safe for concurrent use.
type Manager struct {
	root       string
	defaultBed string
	iso        isolation.Isolator
	shellPath  string
	amenities  *amenity.Registry // nil-safe; ReleaseAll on bed teardown
	commands   *CommandRegistry  // one-shot commands, daemon-global ids
	spawner    Spawner           // forks bed processes; owns the teardown sweep
	resources  resource.Tracker  // per-bed cgroup accounting; noop when unavailable
	maxBeds    int               // cap on concurrent beds; 0 = unlimited
	store      store.Store       // workspace persistence (Noop when disabled)
	// bedIdleTTL is set once at startup. Accepted operations extend their bed
	// through timeout+idleTTL so the idle reaper cannot kill in-flight work.
	bedIdleTTL time.Duration
	// luggage disk watermarks (bytes; high 0 = GC off). Set once at startup
	// via SetLuggageLimits — not synchronized.
	luggageHigh int64
	luggageLow  int64
	// cdpAdvertise (loopback host:port) enables per-bed browser endpoint
	// injection into bed env. Set once at startup via SetCDPAdvertise — not
	// synchronized.
	cdpAdvertise string

	mu   sync.Mutex
	beds map[string]*Bed
	// residentBeds mirrors len(beds) for lock-free instance health reads.
	// Map mutations update it under mu; healthz may observe either side of an
	// in-flight mutation, but never waits behind activation or restore work.
	residentBeds atomic.Int64
}

// ErrBedLimit is returned when creating a new bed would exceed the configured
// cap. Callers should surface it as backpressure (HTTP 429): the upstream
// scheduler is expected to place the sandbox on another instance.
var ErrBedLimit = errors.New("bed: max bed count reached")

// ErrBedUnavailable means the caller holds a stale Bed pointer whose resident
// entry has already been removed.
var ErrBedUnavailable = errors.New("bed: no longer resident")

// NewManager creates the bed manager and ensures the workspace root exists.
// amenities and st may be nil; maxBeds 0 = unlimited.
func NewManager(root, defaultBed, shellPath string, iso isolation.Isolator, amenities *amenity.Registry, maxBeds int, st store.Store) (*Manager, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("bed: create workspace root %s: %w", root, err)
	}
	shellPath = resolveShellPath(shellPath)
	if st == nil {
		st = store.Noop{}
	}
	resources := resource.Noop("resource tracker not configured")
	return &Manager{
		root:       root,
		defaultBed: defaultBed,
		iso:        iso,
		shellPath:  shellPath,
		amenities:  amenities,
		commands:   newCommandRegistry(),
		spawner:    newInProcSpawner(resources),
		resources:  resources,
		maxBeds:    maxBeds,
		store:      st,
		beds:       make(map[string]*Bed),
	}, nil
}

// SetResourceTracker installs host resource accounting before any bed process
// starts. cmd/hostel calls it once during assembly; keeping it out of
// NewManager avoids probing/mutating cgroups in ordinary unit tests.
func (m *Manager) SetResourceTracker(tracker resource.Tracker) {
	if tracker == nil {
		tracker = resource.Noop("resource tracker not configured")
	}
	m.resources = tracker
	m.spawner = newInProcSpawner(tracker)
}

// ResourceReport describes whether exact per-bed accounting is active.
func (m *Manager) ResourceReport() resource.Report { return m.resources.Report() }

// ResourceUsage returns one cumulative usage snapshot for a bed.
func (m *Manager) ResourceUsage(id string) (resource.Usage, error) {
	return m.resources.Usage(id)
}

// Isolator exposes the configured isolator (for /healthz + capabilities).
func (m *Manager) Isolator() isolation.Isolator { return m.iso }

// Amenities exposes the amenity manager (for capabilities + web adapters).
func (m *Manager) Amenities() *amenity.Registry { return m.amenities }

// Commands exposes the one-shot command registry (spec /command endpoints are
// bed-agnostic on status/logs lookups — command ids are daemon-global).
func (m *Manager) Commands() *CommandRegistry { return m.commands }

// MaxBeds reports the configured cap (0 = unlimited) for capacity reporting.
func (m *Manager) MaxBeds() int { return m.maxBeds }

// ResidentBedCount reports the current in-memory bed count without taking the
// manager lock. It is an instance-health fact, not an admission decision.
func (m *Manager) ResidentBedCount() int64 { return m.residentBeds.Load() }

// StoreName reports the persistence backend for capabilities reporting.
func (m *Manager) StoreName() string { return m.store.Name() }

// DefaultBedID reports the id used when a request omits a bed.
func (m *Manager) DefaultBedID() string { return m.defaultBed }

// SetBedIdleTTL configures the idle retention used for new beds and operation
// deadlines. It is startup configuration and must be called before serving.
func (m *Manager) SetBedIdleTTL(ttl time.Duration) { m.bedIdleTTL = ttl }

// Ensure returns the bed for id, creating it on first use. An empty id maps to
// the default bed — so callers that don't know about beds still get one.
func (m *Manager) Ensure(id string) (resolved *Bed, retErr error) {
	if id == "" {
		id = m.defaultBed
	}
	if err := validBedID(id); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.beds[id]; ok {
		b.touch(m.bedIdleTTL)
		return b, nil
	}
	// Cap NEW beds only; the default bed is the single-tenant fallback and
	// must never be refused (a full instance still serves its primary tenant)
	// nor counted — max-beds means "N tenant beds", not "N-1 once the default
	// bed happens to exist".
	if m.maxBeds > 0 && id != m.defaultBed {
		n := len(m.beds)
		if _, ok := m.beds[m.defaultBed]; ok {
			n--
		}
		if n >= m.maxBeds {
			return nil, ErrBedLimit
		}
	}
	trace := beginLifecycle(id, lifecycleActivate)
	defer func() {
		record := trace.finish(lifecycleResult(retErr), retErr)
		if resolved != nil {
			resolved.recordLifecycle(record)
		}
	}()
	bedDir := filepath.Join(m.root, id)
	dataDir := filepath.Join(bedDir, "data")
	if err := os.MkdirAll(bedDir, 0o755); err != nil {
		return nil, fmt.Errorf("bed: create bed dir %s: %w", bedDir, err)
	}
	// Resume: prefer the local copy (luggage) when its generation says it is
	// at least as new as the snapshot — evict→resume on the same instance
	// then costs no download. A stale local copy (the bed ran elsewhere
	// meanwhile) is discarded, never merged. A restore failure fails the
	// ensure — silently starting empty when a snapshot exists would look
	// like data loss.
	restored := false
	var restoreMs int64
	var snapshot *store.SnapshotInfo
	if err := trace.stage("stat_snapshot", func() error {
		var err error
		snapshot, err = m.store.Stat(context.Background(), id)
		return err
	}); err != nil {
		return nil, fmt.Errorf("bed: check snapshot %s: %w", id, err)
	}
	_ = trace.stage("select_source", func() error {
		trace.source = "fresh"
		if snapshot == nil {
			return nil
		}
		trace.source = "luggage"
		local, ok := loadMeta(bedDir)
		if !ok || local.Generation < snapshot.Generation {
			trace.source = "snapshot"
		}
		return nil
	})
	if trace.source == "snapshot" {
		if err := trace.stage("restore", func() error {
			if err := os.RemoveAll(bedDir); err != nil {
				return fmt.Errorf("drop stale luggage: %w", err)
			}
			if err := os.MkdirAll(bedDir, 0o755); err != nil {
				return fmt.Errorf("recreate bed dir: %w", err)
			}
			t0 := time.Now()
			if err := m.store.Restore(context.Background(), id, bedDir); err != nil {
				return err
			}
			restoreMs = time.Since(t0).Milliseconds()
			restored = true
			return nil
		}); err != nil {
			return nil, fmt.Errorf("bed: restore %s: %w", id, err)
		}
	}
	wsDir := filepath.Join(dataDir, "workspace")
	if err := trace.stage("prepare_workspace", func() error {
		if err := os.MkdirAll(wsDir, 0o755); err != nil {
			return err
		}
		// Prepare after restore repopulates the tree and before the bed serves.
		if p, ok := m.iso.(isolation.Preparer); ok {
			return p.Prepare(isolation.Workspace{Home: dataDir, Path: wsDir})
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("bed: prepare workspace %s: %w", id, err)
	}

	now := time.Now()
	var meta bedMeta
	var b *Bed
	if err := trace.stage("commit_resident", func() error {
		var ok bool
		meta, ok = loadMeta(bedDir)
		if !ok {
			meta = bedMeta{Version: 1, BedID: id, CreatedAt: now}
			if err := saveMeta(bedDir, meta); err != nil {
				return err
			}
		}
		// Dirty-tracking baseline: a just-restored bed is in sync NOW; a dir
		// that survived a process restart trusts its on-disk timestamp.
		persistedAt := meta.LastPersistedAt
		if restored || persistedAt.IsZero() {
			persistedAt = now
		}
		usage := meta.Usage
		if restored {
			usage.LastRestoreMs = restoreMs
		}
		retainUntil := time.Time{}
		if m.bedIdleTTL > 0 {
			retainUntil = now.Add(m.bedIdleTTL)
		}
		b = &Bed{
			ID: id, Dir: bedDir, Home: dataDir, Workspace: wsDir,
			CreatedAt: meta.CreatedAt, lastActiveAt: now, retainUntil: retainUntil,
			generation: meta.Generation, persistedAt: persistedAt, usage: usage,
			shells:         make(map[string]*Shell),
			sessions:       make(map[string]*Session),
			inflightByKind: make(map[OperationKind]int),
			paths:          fsops.NewPaths(dataDir, m.iso.MountPoint()),
		}
		m.beds[id] = b
		m.residentBeds.Add(1)
		resolved = b
		return nil
	}); err != nil {
		return nil, fmt.Errorf("bed: write meta %s: %w", id, err)
	}
	// The one place the full id is logged: everything downstream logs Short(),
	// so this line is the grep anchor from a control-plane sandbox id.
	log.Printf("hostel bed resident: bed=%s short=%s restored=%v", id, b.Short(), restored)
	return b, nil
}

// Get returns an existing bed without creating it.
func (m *Manager) Get(id string) (*Bed, bool) {
	if id == "" {
		id = m.defaultBed
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.beds[id]
	return b, ok
}

// List returns a snapshot of all beds.
func (m *Manager) List() []*Bed {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Bed, 0, len(m.beds))
	for _, b := range m.beds {
		out = append(out, b)
	}
	return out
}
