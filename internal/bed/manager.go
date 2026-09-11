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
	"sync"
	"sync/atomic"
	"time"

	"github.com/qiankunli/go-stdx/filepathx"
	"github.com/qiankunli/hostel/internal/amenity"
	"github.com/qiankunli/hostel/internal/executor"
	"github.com/qiankunli/hostel/internal/isolation"
	"github.com/qiankunli/hostel/internal/network"
	"github.com/qiankunli/hostel/internal/privilege"
	"github.com/qiankunli/hostel/internal/resource"
	"github.com/qiankunli/hostel/internal/store"
)

// Manager owns the set of beds and their lifecycle. Safe for concurrent use.
type Manager struct {
	root            string
	defaultBed      string
	iso             isolation.Isolator
	bedUser         privilege.BedUser
	bedUsers        *privilege.BedUserAllocator
	privilegeReport privilege.Report
	diagnosticsMu   sync.RWMutex
	environment     EnvironmentReport
	shellPath       string
	amenities       *amenity.Registry  // nil-safe; ReleaseAll on bed teardown
	executions      *ExecutionRegistry // one-shot executions, daemon-global ids
	executorFactory executor.Factory   // creates each Bed's replaceable process realm
	resources       resource.Tracker   // per-bed cgroup accounting; noop when unavailable
	admission       resource.Admitter  // cached carrier-pressure verdict; never performs request-path I/O
	maxBeds         int                // cap on occupied tenant beds; 0 = unlimited
	maxPinnedBeds   int                // pinned-count pressure reference; 0 = pressure disabled
	pressurePercent int                // shared occupied/pinned high-watermark percentage
	pinnedBeds      atomic.Int64       // tenant beds running work or holding data not yet durable
	network         bedNetwork
	store           *store.Manager // daemon-wide persistence component
	processEnv      processEnv     // explicit carrier software env; never daemon-wide inheritance
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
	// purges fence a bed identity from initialization acceptance until both its
	// local and durable data have been deleted.
	purges      map[string]*bedPurge
	retirements map[string]*Bed // retains identity until destructive cleanup completes
	// luggageCleanups fences a Bed identity while GC removes its cold local
	// tree. The fence is per Bed, so slow filesystem cleanup does not block
	// unrelated Beds.
	luggageCleanups map[string]*luggageCleanup
	// residentBeds tracks resident/evicting tenant beds for lock-free instance
	// health reads. The compatibility default bed is deliberately excluded.
	residentBeds atomic.Int64
}

const defaultBedPressureThresholdPercent = 80

// ErrBedLimit is returned when creating a new bed would exceed the configured
// cap. Callers should surface it as backpressure (HTTP 429): the upstream
// scheduler is expected to place the sandbox on another instance.
var ErrBedLimit = errors.New("bed: max bed count reached")

// ErrResourcePressure is returned when aggregate carrier CPU or memory usage
// is already too high to initialize another tenant bed.
var ErrResourcePressure = errors.New("bed: carrier resource admission threshold reached")

// ErrBedUnavailable means the caller holds a stale Bed pointer whose resident
// entry has already been removed.
var ErrBedUnavailable = errors.New("bed: no longer resident")

// ManagerOption fixes daemon-wide Bed policy during construction, before any
// resident Bed can observe the Manager.
type ManagerOption func(*Manager)

// WithBedUser selects the fixed user inherited by ordinary Bed environments.
// uid isolation resolves a dedicated user from the same policy per Bed.
func WithBedUser(user privilege.BedUser) ManagerOption {
	return func(m *Manager) { m.bedUser = user }
}

// NewManager creates the bed manager and ensures the workspace root exists.
// amenities and st may be nil; maxBeds 0 = unlimited.
func NewManager(root, defaultBed, shellPath string, iso isolation.Isolator, amenities *amenity.Registry, maxBeds int, st *store.Manager, opts ...ManagerOption) (*Manager, error) {
	processEnv, _ := newProcessEnv(os.Environ())
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("bed: create workspace root %s: %w", root, err)
	}
	shellPath = resolveShellPath(shellPath)
	if st == nil {
		st = store.NewManagerWithStores(store.Noop{})
	}
	resources := resource.Noop("resource tracker not configured")
	m := &Manager{
		root:            root,
		defaultBed:      defaultBed,
		iso:             iso,
		bedUser:         privilege.CurrentBedUser(),
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
		pressurePercent: defaultBedPressureThresholdPercent,
		store:           st,
		network:         (*network.Manager)(nil), // nil manager preserves shared networking
		processEnv:      processEnv,
		beds:            make(map[string]*Bed),
		initializations: make(map[string]*bedInitialization),
		purges:          make(map[string]*bedPurge),
		retirements:     make(map[string]*Bed),
		luggageCleanups: make(map[string]*luggageCleanup),
		environment:     EnvironmentReport{ProbeStatus: EnvironmentProbeNotRun},
	}
	for _, option := range opts {
		option(m)
	}
	var effectiveCaps uint64
	if report, ok := iso.(isolation.Report); ok {
		effectiveCaps = report.Facts().EffectiveCaps
	}
	m.bedUsers = isolation.NewBedUserAllocator(m.iso, m.bedUser)
	if err := m.reserveBedUsers(); err != nil {
		return nil, fmt.Errorf("bed: reserve existing users: %w", err)
	}
	m.privilegeReport = privilege.NewReport(isolation.DescribeBedUser(m.iso, m.bedUser), effectiveCaps)
	return m, nil
}

// BedUserReport exposes the configured assignment policy for diagnostics.
func (m *Manager) BedUserReport() privilege.BedUserReport {
	return m.privilegeReport.BedUser
}

func (m *Manager) PrivilegeReport() privilege.Report { return m.privilegeReport }

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

// SetMaxPinnedBeds configures the pinned-count pressure reference. It is startup
// configuration and must be called before serving requests.
func (m *Manager) SetMaxPinnedBeds(max int) error {
	if max < 0 {
		return fmt.Errorf("bed: max pinned beds must be non-negative: %d", max)
	}
	if max == 0 {
		max = m.maxBeds
	}
	m.maxPinnedBeds = max
	return nil
}

// MaxPinnedBeds reports the reference used to derive bed pressure (0 disables
// pinned-count pressure). It is not an admission limit.
func (m *Manager) MaxPinnedBeds() int { return m.maxPinnedBeds }

// SetBedPressureThresholdPercent configures the common occupied/pinned
// high-watermark percentage. Zero disables the bed pressure signal.
func (m *Manager) SetBedPressureThresholdPercent(percent int) error {
	if percent < 0 || percent > 100 {
		return fmt.Errorf("bed: pressure threshold percent must be between 0 and 100: %d", percent)
	}
	m.pressurePercent = percent
	return nil
}

// BedPressureThresholdPercent reports the configured high-watermark percent.
func (m *Manager) BedPressureThresholdPercent() int { return m.pressurePercent }

// PinnedBedCount reports tenant beds that are running an operation or whose
// latest data has not reached the durable store. The default bed is exempt.
func (m *Manager) PinnedBedCount() int64 { return m.pinnedBeds.Load() }

// BedPressure is a scheduling signal. It becomes true when either occupied
// capacity or pinned capacity reaches the configured high-watermark percentage,
// so the upstream can avoid new placement on this carrier. It never rejects
// local work; maxBeds remains the only bed-count hard limit.
//
// +spec=`bed_pressure is true when occupied_beds/max_beds or pinned_beds/max_pinned_beds reaches the configured watermark; pressure never rejects Bed work.`
func (m *Manager) BedPressure() bool {
	if m.pressurePercent <= 0 {
		return false
	}
	pinnedThreshold := bedPressureThreshold(m.maxPinnedBeds, m.pressurePercent)
	if pinnedThreshold > 0 && m.pinnedBeds.Load() >= pinnedThreshold {
		return true
	}
	occupiedThreshold := bedPressureThreshold(m.maxBeds, m.pressurePercent)
	return occupiedThreshold > 0 && int64(m.OccupiedBedCount()) >= occupiedThreshold
}

func bedPressureThreshold(limit, percent int) int64 {
	if limit <= 0 || percent <= 0 {
		return 0
	}
	return (int64(limit)*int64(percent) + 99) / 100
}

// resourceAdmissionErrorLocked applies aggregate CPU/memory admission only
// where this carrier is taking ownership. Pinned count is deliberately absent:
// it is a scheduling hint, while maxBeds is the bed-count admission boundary.
func (m *Manager) resourceAdmissionErrorLocked() error {
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

// ResidentBedCount reports current resident/evicting tenant beds without
// taking the manager lock. The default bed and initializing beds are excluded.
func (m *Manager) ResidentBedCount() int64 { return m.residentBeds.Load() }

// OccupiedBedCount reports tenant beds that currently consume maxBeds
// admission capacity: initializing plus resident/evicting beds.
func (m *Manager) OccupiedBedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tenantOccupiedBedsLocked()
}

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
		if id != m.defaultBed && m.retirements[id] == nil && initialization.status.Phase == PhaseInitializing {
			n++
		}
	}
	for id := range m.retirements {
		if id != m.defaultBed {
			n++
		}
	}
	return n
}

// SyncName reports the default synchronization policy for capabilities reporting.
func (m *Manager) SyncName() string { return string(m.store.DefaultSync()) }

// DefaultBedID reports the id used when a request omits a bed.
func (m *Manager) DefaultBedID() string { return m.defaultBed }

// DefaultBedOccupied reports whether the compatibility bed is resident or
// initializing. It is intentionally separate from Inventory: the default bed
// does not participate in scheduler placement or capacity, but its presence
// still prevents the instance from being safely released.
func (m *Manager) DefaultBedOccupied() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.beds[m.defaultBed]; ok {
		return true
	}
	initialization, ok := m.initializations[m.defaultBed]
	return ok && initialization.status.Phase == PhaseInitializing
}

// SetBedIdleTTL configures the idle retention used for new beds and operation
// deadlines. It is startup configuration and must be called before serving.
func (m *Manager) SetBedIdleTTL(ttl time.Duration) { m.bedIdleTTL = ttl }

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

// Evict releases a bed's compute: run the configured Store persist step, tear
// down, delete the local Bed directory, and free the max-beds slot. Durable
// stores can restore the identity later; noop persists nothing, so a later
// initialization starts fresh. Returns evicted=false
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
	retiring := m.retirements[id]
	m.mu.Unlock()
	if retiring != nil {
		return m.finishRetirement(retiring)
	}
	if !ok {
		canceled, err := m.cancelInitialization(ctx, id)
		if err != nil {
			return false, fmt.Errorf("bed: cancel initialization before evict %s: %w", id, err)
		}
		if canceled {
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
	m.retirements[id] = b
	delete(m.beds, id)
	if id != m.defaultBed {
		m.residentBeds.Add(-1)
	}
	b.mu.Unlock()
	m.mu.Unlock()
	return m.finishRetirement(b)
}

// finishRetirement releases the identity only after its resources and directory
// are gone. A failed attempt stays observable and can be retried with Evict.
// +spec=`Same-ID initialization cannot overlap destructive cleanup from an earlier resident Bed.`
func (m *Manager) finishRetirement(b *Bed) (bool, error) {
	b.cleanupMu.Lock()
	defer b.cleanupMu.Unlock()
	m.mu.Lock()
	current := m.retirements[b.ID] == b
	m.mu.Unlock()
	if !current {
		return true, nil
	}
	if err := m.teardown(b); err != nil {
		return false, err
	}
	if err := os.RemoveAll(b.Dir); err != nil {
		return false, fmt.Errorf("bed: remove local copy %s: %w", b.ID, err)
	}
	m.bedUsers.Release(b.ID)
	m.mu.Lock()
	if m.retirements[b.ID] == b {
		delete(m.retirements, b.ID)
	}
	m.mu.Unlock()
	return true, nil
}

// teardown is retryable. Do not destroy files or networking while a process
// domain may still be using them. Caller serializes retries with cleanupMu.
func (m *Manager) teardown(b *Bed) error {
	if b.runtimeClosed {
		return nil
	}
	if err := m.stopBedTransfers(b.ID); err != nil {
		return fmt.Errorf("bed %s transfer cleanup: %w", b.ID, err)
	}
	m.executions.killBed(b.ID, CauseBedTeardown)
	m.revokeSessions(b)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	err := b.shutdownExecutor(shutdownCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("bed %s executor cleanup: %w", b.ID, err)
	}
	if err := m.amenities.ReleaseAll(b.ID); err != nil {
		return fmt.Errorf("bed %s amenity cleanup: %w", b.ID, err)
	}
	if err := m.resources.Release(b.ID); err != nil {
		return fmt.Errorf("bed %s resource cleanup: %w", b.ID, err)
	}
	networkCtx, cancelNetwork := context.WithTimeout(context.Background(), 5*time.Second)
	err = b.environment.Close(networkCtx)
	cancelNetwork()
	if err != nil {
		return err
	}
	if err := b.BedFS().Close(); err != nil {
		return err
	}
	b.runtimeClosed = true
	return nil
}

// Close is called after HTTP admission stops. Pending retirements retain the
// same cleanup owners and are retried alongside resident Beds.
func (m *Manager) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := m.store.StopTransfers(ctx, ""); err != nil {
		return err
	}
	m.cancelAllInitializations(ctx)
	beds := m.List()
	m.mu.Lock()
	for _, b := range m.retirements {
		beds = append(beds, b)
	}
	m.mu.Unlock()
	var closeErr error
	for _, b := range beds {
		m.executions.killBed(b.ID, CauseDaemonShutdown)
		b.cleanupMu.Lock()
		closeErr = errors.Join(closeErr, m.teardown(b))
		b.cleanupMu.Unlock()
	}
	if closeErr != nil {
		return closeErr
	}
	closeErr = errors.Join(m.network.Close(ctx), m.executorFactory.Close())
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
	b.mu.Lock()
	purging := b.purging
	b.mu.Unlock()
	if purging {
		return ErrBedPurging
	}

	var meta bedMeta
	var snapshotWatermark time.Time
	if err := trace.stage("prepare_snapshot", func() error {
		var ok bool
		meta, ok = loadMeta(b.Dir)
		if !ok {
			meta = bedMeta{Version: 1, BedID: b.ID, CreatedAt: b.CreatedAt, Sync: b.Sync}
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
		if err := m.store.Persist(ctx, b.Sync, b.ID, b.Dir, meta.Generation); err != nil {
			return err
		}
		persistedAt = time.Now()
		// Snapshot facts are scheduler hints, not part of persist correctness.
		// Refresh them at this lifecycle boundary so inventory never performs
		// remote Stat calls on its request path.
		if info, statErr := m.store.Stat(ctx, b.Sync, b.ID); statErr == nil {
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
		if b.Sync == store.SyncNoop {
			// Mixed carriers still need local disk-size facts for noop Beds, but no
			// automatic snapshot generation or Store I/O for them.
			bytes := filepathx.DirBytes(b.Dir)
			b.mu.Lock()
			b.localBytes = bytes
			b.mu.Unlock()
			continue
		}
		b.mu.Lock()
		// A session can stay open indefinitely without writing. Its existence
		// must not block durability; real session traffic touches lastActiveAt,
		// so activity after persistBed captures its watermark keeps the bed
		// dirty for a follow-up pass.
		dirty := b.lastActiveAt.After(b.persistedAt) && b.inflight == 0 && !b.evicting && !b.purging
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
