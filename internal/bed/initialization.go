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
	"time"

	"github.com/qiankunli/hostel/internal/store"
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
	ID string
	BedStatus
	StartedAt time.Time
}

type bedInitialization struct {
	status InitializationStatus
	done   chan struct{}
	cancel context.CancelFunc
	bed    *Bed
	err    error
}

const (
	initializationTimeout         = 5 * time.Minute
	failedInitializationRetention = 10 * time.Minute
)

// InitializeBed accepts the desired Bed identity and starts its initialization
// exactly once. Slow Store work continues independently of the initiating HTTP
// request; callers observe progress through InitializationStatus.
func (m *Manager) InitializeBed(ctx context.Context, id string) (InitializationStatus, error) {
	initialization, resident, err := m.beginInitialization(ctx, id)
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
	initialization, resident, err := m.beginInitialization(ctx, id)
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
		if _, purging := m.purges[id]; purging {
			continue
		}
		statuses = append(statuses, initialization.status)
	}
	for _, purge := range m.purges {
		statuses = append(statuses, purge.status)
	}
	return statuses
}

func (m *Manager) beginInitialization(
	ctx context.Context,
	id string,
) (*bedInitialization, *Bed, error) {
	if id == "" {
		id = m.defaultBed
	}
	if err := validBedID(id); err != nil {
		return nil, nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.Lock()
	m.pruneFailedInitializationsLocked(time.Now())
	if _, purging := m.purges[id]; purging {
		m.mu.Unlock()
		return nil, nil, ErrBedPurging
	}
	if resident, ok := m.beds[id]; ok {
		m.mu.Unlock()
		return nil, resident, nil
	}
	if current, ok := m.initializations[id]; ok && current.status.Phase == PhaseInitializing {
		m.mu.Unlock()
		return current, nil, nil
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
		status: InitializationStatus{
			ID: id,
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
			_ = resident.BedFS().Close()
		}
	}()
	if err := ctx.Err(); err != nil {
		m.finishInitialization(initialization, nil, err)
		return
	}
	m.updateInitialization(initialization, "PublishingResident", "publishing the resident Bed")
	if err := m.publishInitializedBed(initialization, resident); err != nil {
		m.finishInitialization(initialization, nil, err)
		return
	}
	published = true

	// The one full-id log line is the grep anchor from an upstream sandbox id.
	log.Printf("hostel bed resident: bed=%s short=%s", bedID, resident.Short())
	m.finishInitialization(initialization, resident, nil)
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
		BedStatus: status.BedStatus,
	}
}
