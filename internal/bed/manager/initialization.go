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

package manager

import (
	"context"
	"errors"
	"fmt"
	model "github.com/qiankunli/hostel/internal/bed"
	"log"
	"path/filepath"
	"time"

	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/bed/store"
	"reflect"
)

// Phase is the coarse local lifecycle of a Bed identity. Activity
// (active/idle) and readiness remain separate facts.
type Phase = model.LifecyclePhase

const (
	PhaseInitializing = model.PhaseInitializing
	PhaseResident     = model.PhaseResident
	PhaseEvicting     = model.PhaseEvicting
	PhasePurging      = model.PhasePurging
	PhaseDormant      = model.PhaseDormant
	PhaseFailed       = model.PhaseFailed
)

// Readiness reports whether a Bed may receive data-plane operations. Reason is
// stable machine-readable context; Message is diagnostic detail.
type Readiness struct {
	Ready     bool      `json:"status"`
	Reason    string    `json:"reason"`
	Message   string    `json:"message,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// InitializationStatus is the observable result of InitializeBed. A resident
// Bed is returned with PhaseResident and Ready=true; an accepted asynchronous
// initialization returns PhaseInitializing.
type InitializationStatus struct {
	ID   string
	Sync store.SyncKind
	BedStatus
	StartedAt time.Time
}

type bedInitialization struct {
	model     *model.Bed
	local     *localIdentity
	startedAt time.Time
	done      chan struct{}
	cancel    context.CancelFunc
	bed       *managedBed
	err       error
}

const (
	initializationTimeout         = 5 * time.Minute
	failedInitializationRetention = 10 * time.Minute
)

// InitializeBed accepts the desired Bed identity and starts its initialization
// exactly once. Slow Store work continues independently of the initiating HTTP
// request; callers observe progress through InitializationStatus.
func (m *Manager) InitializeBed(ctx context.Context, id string) (InitializationStatus, error) {
	return m.InitializeBedWithOptions(ctx, id, CreateOptions{})
}

func (m *Manager) InitializeBedWithOptions(ctx context.Context, id string, options CreateOptions) (InitializationStatus, error) {
	initialization, resident, err := m.beginInitialization(ctx, id, options)
	if err != nil {
		return InitializationStatus{}, err
	}
	if resident != nil {
		return residentInitializationStatus(resident), nil
	}
	m.mu.Lock()
	status := initialization.snapshot()
	m.mu.Unlock()
	return status, nil
}

// Ensure preserves the native API's create-on-first-use behavior. It shares
// the same initialization as POST /v1/beds, but waits for readiness before
// returning a Bed so no operation can observe a partial BedFS.
func (m *Manager) Ensure(ctx context.Context, id string) (*managedBed, error) {
	initialization, resident, err := m.beginInitialization(ctx, id, CreateOptions{})
	if err != nil || resident != nil {
		return resident, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-initialization.done:
		return initialization.bed, initialization.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Initialization returns an in-flight initialization, purge, or recently
// failed initialization. Resident beds are deliberately read through Get.
func (m *Manager) Initialization(id string) (InitializationStatus, bool) {
	if id == "" {
		id = m.defaultBed
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneFailedInitializationsLocked(time.Now())
	if purge, ok := m.purges[id]; ok {
		return purge.status, true
	}
	if b := m.retirements[id]; b != nil {
		return retirementStatus(b), true
	}
	initialization, ok := m.initializations[id]
	if !ok {
		return InitializationStatus{}, false
	}
	return initialization.snapshot(), true
}

func (m *Manager) initializationStatuses() []InitializationStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.initializationStatusesLocked()
}
func (m *Manager) initializationStatusesLocked() []InitializationStatus {
	m.pruneFailedInitializationsLocked(time.Now())
	statuses := make([]InitializationStatus, 0, len(m.initializations)+len(m.purges))
	for id, initialization := range m.initializations {
		if _, purging := m.purges[id]; purging || m.retirements[id] != nil {
			continue
		}
		statuses = append(statuses, initialization.snapshot())
	}
	for id, b := range m.retirements {
		if m.purges[id] == nil {
			statuses = append(statuses, retirementStatus(b))
		}
	}
	for _, purge := range m.purges {
		statuses = append(statuses, purge.status)
	}
	return statuses
}

func (m *Manager) beginInitialization(
	ctx context.Context,
	id string,
	options CreateOptions,
) (*bedInitialization, *managedBed, error) {
	if err := m.Start(ctx); err != nil {
		return nil, nil, err
	}
	requestedSync := options.Sync
	if options.NetworkPolicy != nil {
		normalized, err := network.NormalizePolicy(*options.NetworkPolicy)
		if err != nil {
			return nil, nil, err
		}
		if !m.network.Status().Enabled {
			return nil, nil, network.ErrUnavailable
		}
		options.NetworkPolicy = &normalized
	}
	if id == "" {
		id = m.defaultBed
	}
	if err := validBedID(id); err != nil {
		return nil, nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// A luggage cleanup owns this Bed identity until its local tree is gone and
	// the UID reservation has been released. Resolve Store state again after a
	// wait because the deleted local metadata may have affected selection.
	var selected store.SyncKind
	var selectionErr error
	for {
		if err := m.waitForLocalCleanup(ctx, id); err != nil {
			return nil, nil, err
		}
		// Resolve outside the manager lock; resident/in-flight Beds below remain
		// authoritative when the request omits Sync.
		selected, selectionErr = m.bedSync(ctx, id, requestedSync)
		if requestedSync != "" && selectionErr != nil {
			return nil, nil, selectionErr
		}
		m.mu.Lock()
		if local := m.localIdentities[id]; local == nil || local.cleanup == nil {
			break
		}
		m.mu.Unlock()
	}
	if m.closed {
		m.mu.Unlock()
		return nil, nil, ErrBedUnavailable
	}
	m.pruneFailedInitializationsLocked(time.Now())
	if _, purging := m.purges[id]; purging {
		m.mu.Unlock()
		return nil, nil, ErrBedPurging
	}

	if resident, ok := m.beds[id]; ok {
		m.mu.Unlock()
		return nil, resident, errors.Join(checkBedSync(requestedSync, selected, resident.Spec().Sync), checkInitialPolicy(options.NetworkPolicy, resident.Spec().NetworkPolicy))
	}
	if current, ok := m.initializations[id]; ok && current.snapshot().Phase == PhaseInitializing {
		m.mu.Unlock()
		return current, nil, errors.Join(checkBedSync(requestedSync, selected, current.snapshot().Sync), checkInitialPolicy(options.NetworkPolicy, current.model.Spec().NetworkPolicy))
	}
	if m.retirements[id] != nil {
		m.mu.Unlock()
		return nil, nil, fmt.Errorf("%w: cleanup pending for %s", ErrBedUnavailable, id)
	}
	if selectionErr != nil {
		m.mu.Unlock()
		return nil, nil, selectionErr
	}
	// A new request retries a failed initialization. Its previous status remains
	// observable until this explicit desired-state signal arrives.
	delete(m.initializations, id)

	// The default Bed is the single-tenant fallback and remains exempt from
	// tenant capacity and pressure admission.
	if m.maxBeds > 0 && id != m.defaultBed && m.tenantOccupiedBedsLocked() >= m.maxBeds {
		m.mu.Unlock()
		return nil, nil, ErrBedLimit
	}
	if id != m.defaultBed {
		if err := m.resourceAdmissionErrorLocked(); err != nil {
			m.mu.Unlock()
			return nil, nil, err
		}
	}

	now := time.Now()
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), initializationTimeout)
	initialization := &bedInitialization{
		local:     m.localIdentityLocked(id),
		startedAt: now,
		done:      make(chan struct{}),
		cancel:    cancel,
	}
	localMeta, localPresent := loadMeta(filepath.Join(m.root, id))
	spec := model.Spec{Dir: filepath.Join(m.root, id), Sync: selected, CreatedAt: localMeta.CreatedAt, LocalPresent: localPresent, LocalGeneration: localMeta.Generation}
	if options.NetworkPolicy != nil {
		spec.NetworkPolicy = network.ToModel(*options.NetworkPolicy)
	}
	initialization.model = initialization.local.bed
	model.SpecWriter{}.Update(initialization.model, func(s *model.Spec) {
		spec.RecoveryDirs = s.RecoveryDirs
		*s = spec
	})
	m.owners.Lifecycle.Set(initialization.model, model.LifecycleStatus{Phase: PhaseInitializing, Reason: "InitializationQueued", UpdatedAt: now})
	m.initializations[id] = initialization
	m.mu.Unlock()

	go m.runInitialization(runCtx, initialization)
	return initialization, nil, nil
}

func (m *Manager) runInitialization(ctx context.Context, initialization *bedInitialization) {
	bedID := initialization.snapshot().ID
	if err := m.saveLocalIdentity(initialization.local); err != nil {
		m.finishInitialization(initialization, nil, fmt.Errorf("save local bed identity: %w", err))
		return
	}
	resident, err := m.initializeResidentBed(ctx, initialization)
	if err != nil {
		m.finishInitialization(initialization, nil, err)
		return
	}
	published := false
	defer func() {
		if !published {
			// Roll back before finishInitialization releases the identity and wakes
			// waiters. Otherwise a same-ID retry could acquire this old network
			// while cleanup is still running. The initialization ctx may be canceled.
			m.mu.Lock()
			m.retirements[bedID] = resident
			m.mu.Unlock()
			cleanupErr := resident.cleanupMu.Acquire(m.cleanupCtx, 1)
			if cleanupErr == nil {
				cleanupErr = m.rollback(resident)
				resident.cleanupMu.Release(1)
			}
			if cleanupErr == nil {
				m.mu.Lock()
				delete(m.retirements, bedID)
				m.mu.Unlock()
			}
			err = errors.Join(err, cleanupErr)
			resident = nil
		}
		m.finishInitialization(initialization, resident, err)
	}()
	if err = ctx.Err(); err != nil {
		return
	}
	m.updateInitialization(initialization, "PublishingResident", "publishing the resident Bed")
	if err = m.publishInitializedBed(initialization, resident); err != nil {
		return
	}
	published = true

	// The one full-id log line is the grep anchor from an upstream sandbox id.
	log.Printf("hostel bed resident: bed=%s short=%s sync=%s", bedID, resident.Short(), resident.Spec().Sync)
}

func (m *Manager) publishInitializedBed(initialization *bedInitialization, resident *managedBed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.initializations[initialization.snapshot().ID]
	if m.closed || !ok || current != initialization || current.snapshot().Phase != PhaseInitializing {
		return context.Canceled
	}
	if _, exists := m.beds[resident.Name]; exists {
		return fmt.Errorf("bed %s became resident during initialization", resident.Name)
	}
	m.owners.Lifecycle.Set(resident.Bed, model.LifecycleStatus{Phase: PhaseResident, Ready: true, Reason: "Initialized", UpdatedAt: time.Now()})
	m.beds[resident.Name] = resident
	delete(m.initializations, resident.Name)
	if resident.Name != m.defaultBed {
		m.residentBeds.Add(1)
		if resident.pinnedLocked() {
			m.pinnedBeds.Add(1)
			m.RequestStoreSync()
		}
	}
	return nil
}

func (m *Manager) finishInitialization(initialization *bedInitialization, resident *managedBed, err error) {
	initialization.cancel()
	failedReason := failedReadinessReason(initialization.snapshot().Readiness.Reason)
	if err != nil {
		m.mu.Lock()
		retiring := m.retirements[initialization.snapshot().ID] != nil
		m.mu.Unlock()
		if !retiring {
			// A failed Stage-in may never create local data. Forget that empty
			// identity before waking retries, but retain any surviving cold copy.
			cleanupCtx, cancel := context.WithTimeout(m.cleanupCtx, 5*time.Second)
			err = errors.Join(err, m.cleanLocalIdentity(cleanupCtx, initialization.local, false))
			cancel()
		}
	}
	m.mu.Lock()
	initialization.bed = resident
	initialization.err = err

	current, currentExists := m.initializations[initialization.snapshot().ID]
	if currentExists && current == initialization {
		if err == nil {
			delete(m.initializations, initialization.snapshot().ID)
		} else if m.retirements[initialization.model.Name] != nil {
			m.owners.Lifecycle.Set(initialization.model, model.LifecycleStatus{Phase: PhaseEvicting, Reason: "CleanupPending", Message: err.Error(), UpdatedAt: time.Now()})
			delete(m.initializations, initialization.model.Name)
		} else if !errors.Is(err, context.Canceled) {
			now := time.Now()
			m.owners.Lifecycle.Set(initialization.model, model.LifecycleStatus{Phase: PhaseFailed, Reason: failedReason, Message: err.Error(), UpdatedAt: now})
		} else {
			delete(m.initializations, initialization.snapshot().ID)
		}
	}
	m.mu.Unlock()
	close(initialization.done)
}

func failedReadinessReason(current string) string {
	switch current {
	case "InspectingSnapshot", "SelectingBedFSSource":
		return "SnapshotInspectionFailed"
	case "RestoringSnapshot":
		return "SnapshotRestoreFailed"
	case "PreparingBedFS":
		return "BedFSPreparationFailed"
	case "PreparingNetwork":
		return "NetworkPreparationFailed"
	case "PreparingResources":
		return "ResourcePreparationFailed"
	case "PublishingResident":
		return "ResidentPublicationFailed"
	default:
		return "InitializationFailed"
	}
}

func (m *Manager) updateInitialization(initialization *bedInitialization, reason, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.initializations[initialization.snapshot().ID]
	if !ok || current != initialization || current.snapshot().Phase != PhaseInitializing {
		return
	}
	m.owners.Lifecycle.Set(current.model, model.LifecycleStatus{Phase: PhaseInitializing, Reason: reason, Message: message, UpdatedAt: time.Now()})
}

func (m *Manager) updateInitializationStageIn(initialization *bedInitialization, step store.StageInStep) {
	switch step {
	case store.StageInInspectSnapshot:
		m.updateInitialization(initialization, "InspectingSnapshot", "checking the durable snapshot")
	case store.StageInSelectSource:
		m.updateInitialization(initialization, "SelectingBedFSSource", "selecting fresh, luggage, or snapshot data")
	case store.StageInRestoreSnapshot:
		m.updateInitialization(initialization, "RestoringSnapshot", "restoring the durable snapshot")
	}
}

func (m *Manager) cancelInitialization(ctx context.Context, id string) (bool, error) {
	m.mu.Lock()
	initialization, ok := m.initializations[id]
	if !ok {
		m.mu.Unlock()
		return false, nil
	}
	if initialization.snapshot().Phase != PhaseInitializing {
		delete(m.initializations, id)
		m.mu.Unlock()
		return true, nil
	}
	initialization.cancel()
	m.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-initialization.done:
		return true, nil
	case <-ctx.Done():
		return true, ctx.Err()
	}
}

func (m *Manager) cancelAllInitializations(ctx context.Context) error {
	m.mu.Lock()
	initializations := make([]*bedInitialization, 0, len(m.initializations))
	for _, initialization := range m.initializations {
		if initialization.snapshot().Phase == PhaseInitializing {
			initialization.cancel()
			initializations = append(initializations, initialization)
		}
	}
	m.mu.Unlock()
	for _, initialization := range initializations {
		select {
		case <-initialization.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (m *Manager) pruneFailedInitializationsLocked(now time.Time) {
	for id, initialization := range m.initializations {
		if initialization.snapshot().Phase == PhaseFailed &&
			now.Sub(initialization.snapshot().Readiness.UpdatedAt) >= failedInitializationRetention {
			delete(m.initializations, id)
		}
	}
}

func residentInitializationStatus(resident *managedBed) InitializationStatus {
	status := resident.Status()
	return InitializationStatus{
		ID:        resident.Name,
		Sync:      resident.Spec().Sync,
		BedStatus: status.BedStatus,
	}
}

// CreateOptions selects the sync policy when creating a Bed. An explicit Sync
// overrides the instance default. After eviction, callers must repeat overrides
// because the local Bed metadata is removed together with its workspace.
type CreateOptions struct {
	Sync          string
	NetworkPolicy *network.Policy
}

var (
	ErrSyncInvalid  = errors.New("bed: unsupported sync kind")
	ErrSyncConflict = errors.New("bed: cannot change an active bed's sync policy")
)

// bedSync uses local metadata for an orphaned Bed. No separate routing record
// outlives its working copy; absent both an override and metadata, use default.
func (m *Manager) bedSync(ctx context.Context, id, requested string) (store.SyncKind, error) {
	if requested == "" {
		meta, _ := loadMeta(filepath.Join(m.root, id))
		requested = string(meta.Sync)
	}
	selected, err := m.store.Resolve(ctx, requested)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrSyncInvalid, err)
	}
	return selected, nil
}

// A create retry may reuse a live Bed, but cannot change its active sync policy.
func checkBedSync(requested string, selected, current store.SyncKind) error {
	if requested != "" && selected != current {
		return ErrSyncConflict
	}
	return nil
}

func retirementStatus(b *managedBed) InitializationStatus {
	return InitializationStatus{ID: b.Name, Sync: b.Spec().Sync, BedStatus: lifecycleView(b.Bed.Status().Lifecycle)}
}

func checkInitialPolicy(requested *network.Policy, initial *model.NetworkPolicy) error {
	if requested != nil && !reflect.DeepEqual(network.ToModel(*requested), initial) {
		return fmt.Errorf("%w: initial policy differs; use the policy API to update a resident Bed", network.ErrInvalidPolicy)
	}
	return nil
}

// snapshot projects lifecycle from the single shared model, including progress.
func (i *bedInitialization) snapshot() InitializationStatus {
	return InitializationStatus{ID: i.model.Name, Sync: i.model.Spec().Sync, BedStatus: lifecycleView(i.model.Status().Lifecycle), StartedAt: i.startedAt}
}
func lifecycleView(s model.LifecycleStatus) BedStatus {
	return BedStatus{Phase: s.Phase, Readiness: Readiness{Ready: s.Ready, Reason: s.Reason, Message: s.Message, UpdatedAt: s.UpdatedAt}}
}
