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
	"path/filepath"
	"time"

	"github.com/qiankunli/hostel/internal/network"
	"github.com/qiankunli/hostel/internal/store"
	"reflect"
)

// Phase is the coarse local lifecycle of a Bed identity. Activity
// (active/idle) and readiness remain separate facts.
type Phase string

const (
	PhaseInitializing Phase = "initializing"
	PhaseResident     Phase = "resident"
	PhaseEvicting     Phase = "evicting"
	PhasePurging      Phase = "purging"
	PhaseDormant      Phase = "dormant"
	PhaseFailed       Phase = "failed"
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
	ID    string
	Store store.Kind
	BedStatus
	StartedAt time.Time
}

type bedInitialization struct {
	initialPolicy *network.Policy
	status        InitializationStatus
	done          chan struct{}
	cancel        context.CancelFunc
	bed           *Bed
	err           error
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
	status := initialization.status
	m.mu.Unlock()
	return status, nil
}

// Ensure preserves the native API's create-on-first-use behavior. It shares
// the same initialization as POST /v1/beds, but waits for readiness before
// returning a Bed so no operation can observe a partial BedFS.
func (m *Manager) Ensure(ctx context.Context, id string) (*Bed, error) {
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
	return initialization.status, true
}

func (m *Manager) initializationStatuses() []InitializationStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneFailedInitializationsLocked(time.Now())
	statuses := make([]InitializationStatus, 0, len(m.initializations)+len(m.purges))
	for id, initialization := range m.initializations {
		if _, purging := m.purges[id]; purging || m.retirements[id] != nil {
			continue
		}
		statuses = append(statuses, initialization.status)
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
) (*bedInitialization, *Bed, error) {
	requestedStore := options.Store
	if options.NetworkPolicy != nil {
		normalized, err := network.NormalizePolicy(*options.NetworkPolicy)
		if err != nil {
			return nil, nil, err
		}
		if !m.network.Report().Enabled {
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

	// Resolve outside the manager lock; resident/in-flight Beds below remain
	// authoritative when the request omits Store.
	selected, selectionErr := m.bedStore(ctx, id, requestedStore)
	if requestedStore != "" && selectionErr != nil {
		return nil, nil, selectionErr
	}
	m.mu.Lock()
	m.pruneFailedInitializationsLocked(time.Now())
	if _, purging := m.purges[id]; purging {
		m.mu.Unlock()
		return nil, nil, ErrBedPurging
	}

	if resident, ok := m.beds[id]; ok {
		m.mu.Unlock()
		return nil, resident, errors.Join(checkBedStore(requestedStore, selected, resident.Store), checkInitialPolicy(options.NetworkPolicy, resident.initialPolicy))
	}
	if current, ok := m.initializations[id]; ok && current.status.Phase == PhaseInitializing {
		m.mu.Unlock()
		return current, nil, errors.Join(checkBedStore(requestedStore, selected, current.status.Store), checkInitialPolicy(options.NetworkPolicy, current.initialPolicy))
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
		initialPolicy: options.NetworkPolicy,
		status: InitializationStatus{
			ID:    id,
			Store: selected,
			BedStatus: BedStatus{
				Phase: PhaseInitializing,
				Readiness: Readiness{
					Reason:    "InitializationQueued",
					UpdatedAt: now,
				},
			},
			StartedAt: now,
		},
		done:   make(chan struct{}),
		cancel: cancel,
	}
	m.initializations[id] = initialization
	m.mu.Unlock()

	go m.runInitialization(runCtx, initialization)
	return initialization, nil, nil
}

func (m *Manager) runInitialization(ctx context.Context, initialization *bedInitialization) {
	bedID := initialization.status.ID
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
			resident.cleanupMu.Lock()
			cleanupErr := m.teardown(resident)
			resident.cleanupMu.Unlock()
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
	if initialization.initialPolicy != nil {
		// Apply before publishing resident: no Bed command can run with allow-all
		// while its requested initial policy is still being installed.
		if _, err = m.network.NetworkPolicy(ctx, bedID, network.PolicyMutation{Replace: initialization.initialPolicy}); err != nil {
			return
		}
		resident.initialPolicy = initialization.initialPolicy
	}
	m.updateInitialization(initialization, "PublishingResident", "publishing the resident Bed")
	if err = m.publishInitializedBed(initialization, resident); err != nil {
		return
	}
	published = true

	// The one full-id log line is the grep anchor from an upstream sandbox id.
	log.Printf("hostel bed resident: bed=%s short=%s store=%s", bedID, resident.Short(), resident.Store)
}

func (m *Manager) publishInitializedBed(initialization *bedInitialization, resident *Bed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.initializations[initialization.status.ID]
	if !ok || current != initialization || current.status.Phase != PhaseInitializing {
		return context.Canceled
	}
	if _, exists := m.beds[resident.ID]; exists {
		return fmt.Errorf("bed %s became resident during initialization", resident.ID)
	}
	m.beds[resident.ID] = resident
	delete(m.initializations, resident.ID)
	if resident.ID != m.defaultBed {
		m.residentBeds.Add(1)
		if resident.pinnedLocked() {
			m.pinnedBeds.Add(1)
			m.RequestStoreSync()
		}
	}
	return nil
}

func (m *Manager) finishInitialization(initialization *bedInitialization, resident *Bed, err error) {
	initialization.cancel()
	m.mu.Lock()
	initialization.bed = resident
	initialization.err = err
	current, currentExists := m.initializations[initialization.status.ID]
	if currentExists && current == initialization {
		if err == nil {
			delete(m.initializations, initialization.status.ID)
		} else if !errors.Is(err, context.Canceled) {
			now := time.Now()
			reason := failedReadinessReason(initialization.status.Readiness.Reason)
			initialization.status.Phase = PhaseFailed
			initialization.status.Readiness = Readiness{
				Reason:    reason,
				Message:   err.Error(),
				UpdatedAt: now,
			}
		} else {
			delete(m.initializations, initialization.status.ID)
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
	current, ok := m.initializations[initialization.status.ID]
	if !ok || current != initialization || current.status.Phase != PhaseInitializing {
		return
	}
	current.status.Readiness.Reason = reason
	current.status.Readiness.Message = message
	current.status.Readiness.UpdatedAt = time.Now()
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
	if initialization.status.Phase != PhaseInitializing {
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

func (m *Manager) cancelAllInitializations(ctx context.Context) {
	m.mu.Lock()
	initializations := make([]*bedInitialization, 0, len(m.initializations))
	for id, initialization := range m.initializations {
		if initialization.status.Phase == PhaseInitializing {
			delete(m.initializations, id)
			initialization.cancel()
			initializations = append(initializations, initialization)
		}
	}
	m.mu.Unlock()
	for _, initialization := range initializations {
		select {
		case <-initialization.done:
		case <-ctx.Done():
			return
		}
	}
}

func (m *Manager) pruneFailedInitializationsLocked(now time.Time) {
	for id, initialization := range m.initializations {
		if initialization.status.Phase == PhaseFailed &&
			now.Sub(initialization.status.Readiness.UpdatedAt) >= failedInitializationRetention {
			delete(m.initializations, id)
		}
	}
}

func residentInitializationStatus(resident *Bed) InitializationStatus {
	status := resident.Status()
	return InitializationStatus{
		ID:        resident.ID,
		Store:     resident.Store,
		BedStatus: status.BedStatus,
	}
}

// CreateOptions selects the backend when creating a Bed. An explicit Store
// overrides the instance default. After eviction, callers must repeat overrides
// because the local Bed metadata is removed together with its workspace.
type CreateOptions struct {
	Store         string
	NetworkPolicy *network.Policy
}

var (
	ErrStoreInvalid  = errors.New("bed: unsupported store kind")
	ErrStoreConflict = errors.New("bed: cannot change an active bed's store")
)

// bedStore uses local metadata for an orphaned Bed. No separate routing record
// outlives its working copy; absent both an override and metadata, use default.
func (m *Manager) bedStore(ctx context.Context, id, requested string) (store.Kind, error) {
	if requested == "" {
		meta, _ := loadMeta(filepath.Join(m.root, id))
		requested = string(meta.Store)
	}
	selected, err := m.store.Resolve(ctx, requested)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrStoreInvalid, err)
	}
	return selected, nil
}

// A create retry may reuse a live Bed, but cannot migrate its active backend.
func checkBedStore(requested string, selected, current store.Kind) error {
	if requested != "" && selected != current {
		return ErrStoreConflict
	}
	return nil
}

func retirementStatus(b *Bed) InitializationStatus {
	return InitializationStatus{ID: b.ID, Store: b.Store, BedStatus: BedStatus{Phase: PhaseEvicting, Readiness: Readiness{Reason: "CleanupPending", Message: "previous Bed resources are being released; retry eviction before reinitializing"}}}
}

func checkInitialPolicy(requested, initial *network.Policy) error {
	if requested != nil && !reflect.DeepEqual(requested, initial) {
		return fmt.Errorf("%w: initial policy differs; use the policy API to update a resident Bed", network.ErrInvalidPolicy)
	}
	return nil
}
