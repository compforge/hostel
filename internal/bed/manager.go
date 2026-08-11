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
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qiankunli/go-stdx/filepathx"
	"github.com/qiankunli/hostel/internal/amenity"
	"github.com/qiankunli/hostel/internal/bedfs"
	"github.com/qiankunli/hostel/internal/executor"
	"github.com/qiankunli/hostel/internal/isolation"
	"github.com/qiankunli/hostel/internal/resource"
	"github.com/qiankunli/hostel/internal/store"
)

// Manager owns the set of beds and their lifecycle. Safe for concurrent use.
type Manager struct {
	root            string
	defaultBed      string
	iso             isolation.Isolator
	shellPath       string
	amenities       *amenity.Registry  // nil-safe; ReleaseAll on bed teardown
	executions      *ExecutionRegistry // one-shot executions, daemon-global ids
	executorFactory executor.Factory   // creates each Bed's replaceable process realm
	resources       resource.Tracker   // per-bed cgroup accounting; noop when unavailable
	admission       resource.Admitter  // cached carrier-pressure verdict; never performs request-path I/O
	maxBeds         int                // cap on resident tenant beds; 0 = unlimited
	maxPinnedBeds   int                // new-resident admission threshold by pinned count; 0 = unlimited
	pinnedBeds      atomic.Int64       // tenant beds running work or holding data not yet durable
	store           store.Store        // workspace persistence (Noop when disabled)
	storeSync       chan struct{}      // coalesced requests; the store loop owns execution cadence
	processEnv      processEnv         // explicit carrier software env; never daemon-wide inheritance
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
	// initializations contains both in-flight work and recently failed status.
	// It is separate from beds so a partially prepared BedFS can never be
	// resolved by data-plane requests.
	initializations map[string]*bedInitialization
	// residentBeds mirrors len(beds) for lock-free instance health reads.
	// Map mutations update it under mu; healthz may observe either side of an
	// in-flight mutation, but never waits behind initialization or restore work.
	residentBeds atomic.Int64
}

// ErrBedLimit is returned when creating a new bed would exceed the configured
// cap. Callers should surface it as backpressure (HTTP 429): the upstream
// scheduler is expected to place the sandbox on another instance.
var ErrBedLimit = errors.New("bed: max bed count reached")

// ErrInsufficientBed is returned when admitting another unpinned tenant bed
// would exceed the configured pinned-bed cap. BED_PRESSURE is only the early
// scheduling signal; this error is the hard admission boundary.
var ErrInsufficientBed = errors.New("bed: insufficient pinned capacity")

// InsufficientBedError freezes the carrier capacity observed at the admission
// boundary. Counts are diagnostic context for the scheduler; the 429 signal
// remains authoritative because inventory reads are deliberately stale-tolerant.
type InsufficientBedError struct {
	PinnedBeds    int64
	MaxPinnedBeds int
	ResidentBeds  int
	MaxBeds       int
}

func (e *InsufficientBedError) Error() string {
	return fmt.Sprintf(
		"%s: pinned_beds=%d max_pinned_beds=%d resident_beds=%d max_beds=%d",
		ErrInsufficientBed, e.PinnedBeds, e.MaxPinnedBeds, e.ResidentBeds, e.MaxBeds,
	)
}

func (e *InsufficientBedError) Unwrap() error { return ErrInsufficientBed }

// ErrResourcePressure is returned when aggregate carrier CPU or memory usage
// is already too high to initialize another tenant bed.
var ErrResourcePressure = errors.New("bed: carrier resource admission threshold reached")

// ErrBedUnavailable means the caller holds a stale Bed pointer whose resident
// entry has already been removed.
var ErrBedUnavailable = errors.New("bed: no longer resident")

// NewManager creates the bed manager and ensures the workspace root exists.
// amenities and st may be nil; maxBeds 0 = unlimited.
func NewManager(root, defaultBed, shellPath string, iso isolation.Isolator, amenities *amenity.Registry, maxBeds int, st store.Store) (*Manager, error) {
	processEnv, err := newProcessEnv(os.Environ(), []string{"PATH"})
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("bed: create workspace root %s: %w", root, err)
	}
	shellPath = resolveShellPath(shellPath)
	if st == nil {
		st = store.Noop{}
	}
	resources := resource.Noop("resource tracker not configured")
	return &Manager{
		root:            root,
		defaultBed:      defaultBed,
		iso:             iso,
		shellPath:       shellPath,
		amenities:       amenities,
		executions:      newExecutionRegistry(),
		executorFactory: executor.NewLocalFactory(resources),
		resources:       resources,
		admission:       resource.NoopAdmission("resource admission not configured"),
		maxBeds:         maxBeds,
		// Zero max-pinned-beds inherits this value; initialize the effective
		// default here so direct Manager users get the same semantics as main.
		maxPinnedBeds:   maxBeds,
		store:           st,
		storeSync:       make(chan struct{}, 1),
		processEnv:      processEnv,
		beds:            make(map[string]*Bed),
		initializations: make(map[string]*bedInitialization),
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
	m.executorFactory = executor.NewLocalFactory(tracker)
}

// SetExecutorFactory selects the process-realm implementation before serving.
func (m *Manager) SetExecutorFactory(factory executor.Factory) {
	if factory == nil {
		factory = executor.NewLocalFactory(m.resources)
	}
	m.executorFactory = factory
}

// ResourceReport describes whether exact per-bed accounting is active.
func (m *Manager) ResourceReport() resource.Report { return m.resources.Report() }

// ResourceUsage returns one cumulative usage snapshot for a bed.
func (m *Manager) ResourceUsage(id string) (resource.Usage, error) {
	return m.resources.Usage(id)
}

// SetResourceAdmission installs the cached carrier-pressure gate before
// serving requests. The sampler itself is owned by internal/resource.
func (m *Manager) SetResourceAdmission(admission resource.Admitter) {
	if admission == nil {
		admission = resource.NoopAdmission("resource admission not configured")
	}
	m.admission = admission
}

// ResourceAdmissionReport returns the latest carrier-pressure sample.
func (m *Manager) ResourceAdmissionReport() resource.AdmissionReport {
	return m.admission.Report()
}

// Isolator exposes the configured isolator (for /healthz + capabilities).
func (m *Manager) Isolator() isolation.Isolator { return m.iso }

// Amenities exposes the amenity manager (for capabilities + web adapters).
func (m *Manager) Amenities() *amenity.Registry { return m.amenities }

// Executions exposes the bounded one-shot execution registry. IDs are daemon
// global because status/log endpoints do not carry a bed dimension.
func (m *Manager) Executions() *ExecutionRegistry { return m.executions }

// ExecutorBackend reports the configured process-realm implementation.
func (m *Manager) ExecutorBackend() string { return m.executorFactory.Backend() }

// MaxBeds reports the configured cap (0 = unlimited) for capacity reporting.
func (m *Manager) MaxBeds() int { return m.maxBeds }

// SetMaxPinnedBeds configures new-resident admission. It is startup
// configuration and must be called before serving requests.
func (m *Manager) SetMaxPinnedBeds(max int) error {
	if max < 0 {
		return fmt.Errorf("bed: max pinned beds must be non-negative: %d", max)
	}
	if max == 0 || (m.maxBeds > 0 && max > m.maxBeds) {
		max = m.maxBeds
	}
	m.maxPinnedBeds = max
	return nil
}

// MaxPinnedBeds reports the pinned-count threshold that stops new resident
// admission (0 = unlimited). Existing residents may run above it.
func (m *Manager) MaxPinnedBeds() int { return m.maxPinnedBeds }

// PinnedBedCount reports tenant beds that are running an operation or whose
// latest data has not reached the durable store. The default bed is exempt.
func (m *Manager) PinnedBedCount() int64 { return m.pinnedBeds.Load() }

// BedPressure is an early scheduling signal. It becomes true at 80% of the
// pinned-bed cap so the upstream can warm another carrier before hard admission
// is exhausted. The remaining capacity is reserved for source-carrier fallback.
func (m *Manager) BedPressure() bool {
	threshold := bedPressureThreshold(m.maxPinnedBeds)
	return threshold > 0 && m.pinnedBeds.Load() >= threshold
}

const bedPressurePercent = 80

func bedPressureThreshold(maxPinnedBeds int) int64 {
	if maxPinnedBeds <= 0 {
		return 0
	}
	return (int64(maxPinnedBeds)*bedPressurePercent + 99) / 100
}

// carrierAdmissionErrorLocked applies pressure only where this carrier is
// taking ownership: a new resident, a dormant restore, or an unpinned idle
// cache becoming active again. Callers hold m.mu so the count snapshot is coherent
// with resident admission.
func (m *Manager) carrierAdmissionErrorLocked() error {
	if m.maxPinnedBeds > 0 && m.pinnedBeds.Load() >= int64(m.maxPinnedBeds) {
		m.RequestStoreSync()
		return &InsufficientBedError{
			PinnedBeds:    m.pinnedBeds.Load(),
			MaxPinnedBeds: m.maxPinnedBeds,
			ResidentBeds:  m.tenantResidentBedsLocked(),
			MaxBeds:       m.maxBeds,
		}
	}
	if decision := m.admission.Check(); !decision.Allowed {
		m.RequestStoreSync()
		return fmt.Errorf("%w: %s", ErrResourcePressure, decision.Reason)
	}
	return nil
}

// adjustPinnedLocked updates the tenant capacity counter after a bed mutation.
// Callers hold m.mu and b.mu and pass the pre-mutation compound value.
func (m *Manager) adjustPinnedLocked(b *Bed, wasPinned bool) {
	if b.ID == m.defaultBed {
		return
	}
	isPinned := b.pinnedLocked()
	switch {
	case !wasPinned && isPinned:
		m.pinnedBeds.Add(1)
	case wasPinned && !isPinned:
		m.pinnedBeds.Add(-1)
	}
}

// ResidentBedCount reports the current in-memory bed count without taking the
// manager lock. It is an instance-health fact, not an admission decision.
func (m *Manager) ResidentBedCount() int64 { return m.residentBeds.Load() }

// tenantResidentBedsLocked returns the count governed by maxBeds. The default
// compatibility bed is resident but deliberately exempt from tenant capacity.
// Callers must hold m.mu.
func (m *Manager) tenantResidentBedsLocked() int {
	n := len(m.beds)
	if _, ok := m.beds[m.defaultBed]; ok {
		n--
	}
	return n
}

// tenantOccupiedBedsLocked includes initializing beds because admission must
// reserve their eventual resident slot before any slow Store I/O starts.
// Callers must hold m.mu.
func (m *Manager) tenantOccupiedBedsLocked() int {
	n := m.tenantResidentBedsLocked()
	for id, initialization := range m.initializations {
		if id != m.defaultBed && initialization.status.Phase == PhaseInitializing {
			n++
		}
	}
	return n
}

// StoreName reports the persistence backend for capabilities reporting.
func (m *Manager) StoreName() string { return m.store.Name() }

// DefaultBedID reports the id used when a request omits a bed.
func (m *Manager) DefaultBedID() string { return m.defaultBed }

// SetBedIdleTTL configures the idle retention used for new beds and operation
// deadlines. It is startup configuration and must be called before serving.
func (m *Manager) SetBedIdleTTL(ttl time.Duration) { m.bedIdleTTL = ttl }

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
	// Resume: prefer the local copy (luggage) when its generation says it is
	// at least as new as the snapshot — evict→resume on the same instance
	// then costs no download. A stale local copy (the bed ran elsewhere
	// meanwhile) is replaced atomically, never merged. A restore failure leaves
	// the old luggage untouched and fails initialization rather than silently
	// starting empty.
	local, localPresent := loadMeta(bedDir)
	var staged store.StageInResult
	if err := trace.stage("stage_in_bedfs", func() error {
		var err error
		staged, err = store.StageInBedFS(ctx, m.store, store.StageInRequest{
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
	if err := trace.stage("prepare_bedfs", func() error {
		if err := os.MkdirAll(wsDir, 0o755); err != nil {
			return err
		}
		// Prepare after restore repopulates the tree and before the bed serves.
		if p, ok := m.iso.(isolation.Preparer); ok {
			return p.Prepare(bedfs.New(dataDir))
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("bed: prepare workspace %s: %w", id, err)
	}

	now := time.Now()
	var meta bedMeta
	var b *Bed
	if err := trace.stage("prepare_resident", func() error {
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
			durable:            m.store.Name() != "noop",
			shells:             make(map[string]*Shell),
			sessions:           make(map[string]*Session),
			inflightByKind:     make(map[OperationKind]int),
			filesystem:         bedfs.New(dataDir),
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
		return nil, fmt.Errorf("bed: write meta %s: %w", id, err)
	}
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

// Evict releases a bed's compute while keeping its identity (IDLE →
// EVICTING → LUGGAGE, docs/store.md §4): persist, then tear down and
// free the max-beds slot. The local dir stays behind as luggage — a warm
// cache of the DORMANT bed, so a same-instance resume skips the snapshot
// download; luggage GC reclaims disk separately. Returns evicted=false
// without error when the eviction was CANCELED because the bed saw new
// activity during the persist window — serving beats reclaiming, and
// removing runtime state after a mid-persist write would silently drop that
// write. A persist failure aborts the evict (never destroy the only copy).
func (m *Manager) Evict(ctx context.Context, id string) (bool, error) {
	return m.evict(ctx, id, nil)
}

// evictExpired is the idle-GC path. Unlike explicit Evict, it atomically
// re-checks the bed's deadline and running exec count before entering EVICTING.
func (m *Manager) evictExpired(ctx context.Context, id string, now time.Time) (bool, error) {
	return m.evict(ctx, id, &now)
}

func (m *Manager) evict(ctx context.Context, id string, expiryCutoff *time.Time) (evicted bool, retErr error) {
	if id == "" {
		id = m.defaultBed
	}
	m.mu.Lock()
	b, ok := m.beds[id]
	m.mu.Unlock()
	if !ok {
		if m.cancelInitialization(ctx, id) {
			return true, nil
		}
		return false, nil // not resident; nothing to evict
	}
	trace := beginLifecycle(ctx, id, lifecycleEvict)
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

	if err := m.persistBed(ctx, b, "evict"); err != nil {
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

const purgeStoreTimeout = 30 * time.Second

// Purge ends a bed's identity: tear down (no persist), remove the local dir
// (active workspace or leftover luggage), and delete the snapshot. Explicitly
// destructive — the caller asked for the data to be gone, so concurrent
// activity does not cancel it.
func (m *Manager) Purge(ctx context.Context, id string) error {
	if id == "" || id == m.defaultBed {
		return ErrPurgeDefault
	}
	// Purge touches the filesystem even for beds not in memory (luggage), so
	// the id must be validated here too — never path-join an unchecked id.
	if err := validBedID(id); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Purge must join an in-flight initializer before removing the directory;
	// otherwise a canceled request could return while Restore recreates data
	// after the identity was declared gone.
	cancelCtx, cancelInitialization := context.WithTimeout(context.WithoutCancel(ctx), purgeStoreTimeout)
	m.cancelInitialization(cancelCtx, id)
	cancelInitialization()
	m.mu.Lock()
	b, ok := m.beds[id]
	if ok {
		b.mu.Lock()
		delete(m.beds, id)
		m.residentBeds.Add(-1)
		if b.pinnedLocked() {
			m.pinnedBeds.Add(-1)
		}
		b.mu.Unlock()
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
	deleteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), purgeStoreTimeout)
	defer cancel()
	return m.store.Delete(deleteCtx, id)
}

// teardown kills a bed's runtime state: shells, one-shot commands, service
// tenants. The workspace is untouched — callers decide its fate. On the
// evict path the shells are already closed (revoke stage, before persist);
// the close here covers Purge, which has no revoke.
func (m *Manager) teardown(b *Bed) {
	// Publish the controller's intent before any Shell/Executor action can make
	// a waiter observe EOF or SIGKILL. The first terminal actor owns Cause.
	m.executions.killBed(b.ID, CauseBedTeardown)
	b.mu.Lock()
	for sid, sh := range b.shells {
		sh.Close()
		delete(b.shells, sid)
	}
	b.mu.Unlock()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = b.shutdownExecutor(shutdownCtx)
	cancel()
	m.amenities.ReleaseAll(b.ID)
}

// Close drains every resident Bed's process realm. HTTP admission must already
// be stopped so no fresh Executor can appear while shutdown is in progress.
func (m *Manager) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.cancelAllInitializations(ctx)
	var closeErr error
	for _, b := range m.List() {
		m.executions.killBed(b.ID, CauseDaemonShutdown)
		m.revokeSessions(b)
		if err := b.shutdownExecutor(ctx); err != nil {
			closeErr = errors.Join(closeErr, fmt.Errorf("bed %s executor: %w", b.ID, err))
		}
		m.amenities.ReleaseAll(b.ID)
	}
	if err := m.executorFactory.Close(); err != nil {
		closeErr = errors.Join(closeErr, err)
	}
	return closeErr
}

// CollectExpired reaps beds whose promised expiry has elapsed. The final
// expiry/running-exec check happens again under the bed lock in evictExpired,
// closing the scan→evict race with a concurrent command.
// The default bed is never reaped.
func (m *Manager) CollectExpired(ctx context.Context, now time.Time) []string {
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
		if ok, _ := m.evictExpired(ctx, id, now); ok {
			reaped = append(reaped, id)
		}
	}
	return reaped
}

// persistBed snapshots the bed dir (portable meta + data) and, on success,
// advances both the in-memory and on-disk persistence watermarks.
//
// Ordering constraint: the generation bump is saved BEFORE packing (the
// snapshot must carry its own generation), but LastPersistedAt only AFTER a
// successful upload — a failed upload leaving the local generation ahead is
// accurate ("locally dirty"), while a falsely-advanced LastPersistedAt would
// make restart-time dirty tracking skip data that never reached the store.
func (m *Manager) persistBed(ctx context.Context, b *Bed, trigger string) (retErr error) {
	trace := beginLifecycle(ctx, b.ID, lifecyclePersist)
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
	var snapshotWatermark time.Time
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
		snapshotWatermark = b.lastActiveAt
		b.mu.Unlock()
		return nil
	}); err != nil {
		return fmt.Errorf("bed: bump generation %s: %w", b.ID, err)
	}

	var persistedAt time.Time
	var persistStarted time.Time
	var snapshot *store.SnapshotInfo
	if err := trace.stage("persist_store", func() error {
		persistStarted = time.Now()
		if err := m.store.Persist(ctx, b.ID, b.Dir, meta.Generation); err != nil {
			return err
		}
		persistedAt = time.Now()
		// Snapshot facts are scheduler hints, not part of persist correctness.
		// Refresh them at this lifecycle boundary so inventory never performs
		// remote Stat calls on its request path.
		if info, statErr := m.store.Stat(ctx, b.ID); statErr == nil {
			snapshot = info
		} else {
			log.Printf("hostel: refresh snapshot facts failed: bed=%s error=%v", b.Short(), statErr)
		}
		return nil
	}); err != nil {
		return err
	}
	localBytes := filepathx.DirBytes(b.Dir)

	_ = trace.stage("commit_watermark", func() error {
		m.mu.Lock()
		b.mu.Lock()
		wasPinned := b.pinnedLocked()
		b.persistedAt = snapshotWatermark
		b.usage.LastPersistMs = persistedAt.Sub(persistStarted).Milliseconds()
		b.localBytes = localBytes
		if snapshot != nil {
			b.snapshotGeneration = snapshot.Generation
			b.snapshotBytes = snapshot.Bytes
			meta.SnapshotGeneration = snapshot.Generation
			meta.SnapshotBytes = snapshot.Bytes
		}
		meta.Usage = b.usage
		if current, ok := m.beds[b.ID]; ok && current == b {
			m.adjustPinnedLocked(b, wasPinned)
		}
		b.mu.Unlock()
		m.mu.Unlock()
		meta.LastPersistedAt = snapshotWatermark
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

// PersistDirty asks the persistence subsystem to snapshot every idle bed
// touched since its last snapshot. The store loop normally owns invocation;
// this synchronous entry point is retained for tests and explicit callers.
// The default bed is included because its data matters most.
func (m *Manager) PersistDirty(ctx context.Context) []string {
	done, _ := m.persistDirty(ctx, "manual")
	return done
}

func (m *Manager) persistDirty(ctx context.Context, trigger string) ([]string, bool) {
	var done []string
	failed := false
	for _, b := range m.List() {
		b.mu.Lock()
		// A session can stay open indefinitely without writing. Its existence
		// must not block durability; real session traffic touches lastActiveAt,
		// so activity after persistBed captures its watermark keeps the bed
		// dirty for a follow-up pass.
		dirty := b.lastActiveAt.After(b.persistedAt) && b.inflight == 0 && !b.evicting
		b.mu.Unlock()
		if !dirty {
			continue
		}
		if err := m.persistBed(ctx, b, trigger); err != nil {
			log.Printf("hostel: store sync failed: bed=%s trigger=%s error=%v", b.Short(), trigger, err)
			failed = true
			continue
		}
		done = append(done, b.ID)
	}
	return done, failed
}
