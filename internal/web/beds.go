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

package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/qiankunli/go-stdx/randx"

	bed "github.com/qiankunli/hostel/internal/bed/manager"
	"github.com/qiankunli/hostel/internal/bed/network"
)

// bedView is the JSON shape for a bed in the management API.
type bedView struct {
	Sync         string        `json:"sync"`
	ID           string        `json:"id"`
	Status       bed.BedStatus `json:"status"`
	DataSynced   bool          `json:"data_synced"`
	Pinned       bool          `json:"pinned"`
	Workspace    string        `json:"workspace,omitempty"`
	CreatedAt    time.Time     `json:"created_at,omitzero"`
	LastActiveAt time.Time     `json:"last_active_at,omitzero"`
	RetainUntil  time.Time     `json:"retained_until,omitzero"`
}

func (s *Server) viewOf(b *bed.Resident) bedView {
	return s.viewFromStatus(b, b.Status())
}

func (s *Server) viewFromStatus(b *bed.Resident, status bed.ResidentStatus) bedView {
	return bedView{
		ID:           b.Name,
		Sync:         string(b.Spec().Sync),
		Status:       status.BedStatus,
		DataSynced:   status.DataSynced,
		Pinned:       status.Pinned,
		Workspace:    b.Workspace(),
		CreatedAt:    b.Spec().CreatedAt,
		LastActiveAt: status.LastActiveAt,
		RetainUntil:  status.RetainUntil,
	}
}

func initializationView(status bed.InitializationStatus) bedView {
	return bedView{ID: status.ID, Sync: string(status.Sync), Status: status.BedStatus}
}

type lifecycleStageView struct {
	Name       string `json:"name"`
	Result     string `json:"result"`
	DurationMs int64  `json:"duration_ms"`
}

type lifecycleRecordView struct {
	Action      string               `json:"action"`
	Result      string               `json:"result"`
	Source      string               `json:"source,omitempty"`
	Trigger     string               `json:"trigger,omitempty"`
	StartedAt   time.Time            `json:"started_at"`
	FinishedAt  time.Time            `json:"finished_at"`
	DurationMs  int64                `json:"duration_ms"`
	Stages      []lifecycleStageView `json:"stages"`
	FailedStage string               `json:"failed_stage,omitempty"`
	Error       string               `json:"error,omitempty"`
}

type lifecycleView struct {
	LastInitialization *lifecycleRecordView `json:"last_initialization,omitempty"`
	LastPersist        *lifecycleRecordView `json:"last_persist,omitempty"`
}

// activityView is what the bed is doing right now, by request category
// (docs/kernel.md): operations are in-flight stateless requests, sessions
// are open stateful holds. Sessions never raise the bed's activity — an idle bed
// may still hold cdp connections.
type activityView struct {
	Operations map[bed.OperationKind]int `json:"operations,omitempty"`
	Sessions   map[bed.SessionKind]int   `json:"sessions,omitempty"`
}

type executorView struct {
	ID      string `json:"id"`
	Backend string `json:"backend"`
	State   string `json:"state"`
}

type bedDetailView struct {
	bedView
	Generation         int64          `json:"generation"`
	SnapshotGeneration int64          `json:"snapshot_generation,omitempty"`
	SnapshotBytes      int64          `json:"snapshot_bytes,omitempty"`
	LocalBytes         int64          `json:"local_bytes,omitempty"`
	RestoreBytes       int64          `json:"restore_bytes,omitempty"`
	Activity           activityView   `json:"activity"`
	Lifecycle          *lifecycleView `json:"lifecycle,omitempty"`
	Executor           *executorView  `json:"executor,omitempty"`
}

// instanceStatus is the hostel-layer status (docs/kernel.md): the only way
// a hostel says "you may release me". The verdict is computed here so upstream
// reads a conclusion instead of reassembling phase/activity counts and luggage.
// GET /v1/beds — the scheduler's one-poll picture: instance capacity plus
// every bed this instance holds (resident/evicting plus activity, dormant as
// luggage on disk) with its last persisted generation. Everything here is a
// stale-tolerant hint — freshness is re-enforced at initialization, so routing on
// outdated data is slow, never wrong. Normal eviction removes the local Bed
// directory for every Store backend; dormant rows only describe orphaned
// directories discovered after an unclean shutdown or upgrade.
//
// +spec=`phase_counts and activity_counts are exact projections of the beds returned in the same response.`
// +case:id=bed_inventory_invariants,desc=`Create, activate, idle, and purge beds`,expect=`instance counters match the returned bed facts after every transition`
func (s *Server) bedList(c *gin.Context) {
	c.JSON(http.StatusOK, s.mgr.InventoryStatus())
}

type createBedRequest struct {
	NetworkPolicy *network.Policy `json:"networkPolicy,omitempty"`
	ID            string          `json:"id,omitempty"`
	Sync          string          `json:"sync,omitempty"`
}

// POST /v1/beds — create (or return existing) a bed. Empty id → server-assigned.
//
// +spec=`A new bed reserves capacity before initialization and is published only after readiness succeeds.`
// +case:id=bed_capacity_limit,desc=`Fill max_beds and create one more bed`,expect=`429 BED_LIMIT_EXCEEDED without exceeding resident capacity`
func (s *Server) bedCreate(c *gin.Context) {
	var req createBedRequest
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		badRequest(c, "invalid create bed request")
		return
	}
	id := req.ID
	if id == "" {
		id = "bed-" + randx.Hex(6)
	}
	status, err := s.mgr.InitializeBedWithOptions(c.Request.Context(), id, bed.CreateOptions{Sync: req.Sync, NetworkPolicy: req.NetworkPolicy})
	if err != nil {
		if errors.Is(err, network.ErrUnavailable) {
			respondError(c, http.StatusServiceUnavailable, ErrServiceUnavailable, "bed network policy is unavailable")
		} else if errors.Is(err, network.ErrInvalidPolicy) {
			badRequest(c, err.Error())
		} else {
			respondBedError(c, err)
		}
		return
	}
	if status.Readiness.Ready {
		b, ok := s.mgr.Get(id)
		if !ok {
			runtimeError(c, "initialized bed is not resident")
			return
		}
		c.JSON(http.StatusOK, s.viewOf(b))
		return
	}
	c.JSON(http.StatusAccepted, initializationView(status))
}

// GET /v1/beds/:bedId
func (s *Server) bedGet(c *gin.Context) {
	b, ok := s.mgr.Get(c.Param("bedId"))
	if !ok {
		if initialization, initializing := s.mgr.Initialization(c.Param("bedId")); initializing {
			c.JSON(http.StatusOK, initializationView(initialization))
			return
		}
		respondError(c, http.StatusNotFound, ErrBedInvalid, "bed not found")
		return
	}
	status := b.Status()
	lifecycle := b.Lifecycle()
	c.JSON(http.StatusOK, bedDetailView{
		bedView:            s.viewFromStatus(b, status),
		Generation:         status.Generation,
		SnapshotGeneration: status.SnapshotGeneration,
		SnapshotBytes:      status.SnapshotBytes,
		LocalBytes:         status.LocalBytes,
		RestoreBytes:       status.RestoreBytes(),
		Activity: activityView{
			Operations: status.Operations,
			Sessions:   status.Sessions,
		},
		Lifecycle: &lifecycleView{
			LastInitialization: lifecycleRecordToView(lifecycle.LastInitialization),
			LastPersist:        lifecycleRecordToView(lifecycle.LastPersist),
		},
		Executor: executorViewFromStatus(status.Executor),
	})
}

func executorViewFromStatus(status *bed.ExecutorStatus) *executorView {
	if status == nil {
		return nil
	}
	return &executorView{ID: status.ID, Backend: status.Backend, State: string(status.State)}
}

func lifecycleRecordToView(record *bed.LifecycleRecord) *lifecycleRecordView {
	if record == nil {
		return nil
	}
	stages := make([]lifecycleStageView, 0, len(record.Stages))
	for _, stage := range record.Stages {
		stages = append(stages, lifecycleStageView{
			Name:       stage.Name,
			Result:     stage.Result,
			DurationMs: stage.Duration.Milliseconds(),
		})
	}
	return &lifecycleRecordView{
		Action:      record.Action,
		Result:      record.Result,
		Source:      record.Source,
		Trigger:     record.Trigger,
		StartedAt:   record.StartedAt,
		FinishedAt:  record.FinishedAt,
		DurationMs:  record.Duration.Milliseconds(),
		Stages:      stages,
		FailedStage: record.FailedStage,
		Error:       record.Error,
	}
}

// DELETE /v1/beds/:bedId — evict by default (persist, release compute, keep
// the snapshot identity); ?purge=true ends the identity (snapshot deleted
// too). An evict canceled by concurrent bed activity returns 409 BED_BUSY —
// stop sending traffic, then retry.
//
// +spec=`Eviction never tears down a bed that became active during the eviction fence; purge removes the bed identity after work is idle.`
// +case:id=active_bed_evict_busy,desc=`Evict while a background execution is active`,expect=`409 BED_BUSY and the execution remains owned by the bed`
// +case:id=evict_resume_store_semantics,desc=`Evict an idle bed and initialize the same id again`,expect=`durable stores restore the snapshot while noop starts fresh; neither retains the normal local directory`
// +case:id=purge_removes_bed,desc=`Purge idle beds`,expect=`the beds disappear from inventory`
func (s *Server) bedDelete(c *gin.Context) {
	id := c.Param("bedId")
	if c.Query("purge") == "true" {
		if c.Query("store") != "" {
			badRequest(c, "use sync to select the Bed synchronization policy")
			return
		}
		if err := s.mgr.PurgeWithSync(c.Request.Context(), id, c.Query("sync")); err != nil {
			if errors.Is(err, bed.ErrSyncInvalid) || errors.Is(err, bed.ErrSyncConflict) {
				respondBedError(c, err)
				return
			}
			if errors.Is(err, bed.ErrPurgeDefault) {
				badRequest(c, err.Error())
				return
			}
			runtimeError(c, err.Error())
			return
		}
		c.Status(http.StatusOK)
		return
	}
	evicted, err := s.mgr.Evict(c.Request.Context(), id)
	if err != nil {
		runtimeError(c, err.Error())
		return
	}
	if !evicted {
		if _, ok := s.mgr.Get(id); ok {
			respondError(c, http.StatusConflict, ErrBedBusy, "bed saw activity during eviction; retry after traffic stops")
			return
		}
		// Not resident at all — idempotent delete.
	}
	c.Status(http.StatusOK)
}

// GET /v1/beds/capabilities — what this hostel can do (SDK feature detection).
func (s *Server) capabilities(c *gin.Context) {
	iso := s.mgr.Isolator()
	resources := s.mgr.ResourceReport()
	amenities := map[string]string{} // name → lifecycle state
	for _, a := range s.mgr.Amenities().List() {
		amenities[a.Name()] = a.State()
	}
	c.JSON(http.StatusOK, gin.H{
		"isolator":    iso.Name(),
		"isolator_ok": iso.Available(),
		// True when the bed workspace is mounted at the canonical /workspace
		// inside the sandbox (bwrap): shell paths == file-API paths. False
		// under direct, where /workspace is only the file-API virtual prefix.
		"workspace_mount":                iso.WorkspaceMounted(),
		"workspace_view":                 workspaceView(iso),
		"executor_backend":               s.mgr.ExecutorBackend(),
		"max_beds":                       s.mgr.MaxBeds(),
		"max_pinned_beds":                s.mgr.MaxPinnedBeds(),
		"bed_pressure_threshold_percent": s.mgr.BedPressureThresholdPercent(),
		"persistence":                    s.mgr.SyncName(),
		"bed_sync_selection":             true,
		"transfer_syncs":                 []string{"copy", "restic"},
		"file_transfers":                 s.mgr.TransfersConfigured(),
		"transfer_instance_id":           s.mgr.TransferInstanceID(),
		"network":                        s.mgr.NetworkReport(),
		"network_policy":                 true,
		"resource_accounting": gin.H{
			"backend":   resources.Backend,
			"available": resources.Available,
			"reason":    resources.Reason,
		},
		"resource_admission": resourceAdmissionView(s.mgr.ResourceAdmissionReport()),
		"files":              true,
		"directories":        true,
		"command":            true,
		"session":            true,
		"beds":               true,
		"amenities":          amenities, // name → unavailable|idle|running
		// Explicitly-not-yet capabilities, so SDKs don't probe blindly.
		"pty":            false,
		"code":           false,
		"overlay_commit": false,
	})
}

// POST /v1/beds/:bedId/checkpoint — snapshot the bed's workspace now, without
// tearing it down. 200 with the persistence backend on success.
func (s *Server) bedCheckpoint(c *gin.Context) {
	id := c.Param("bedId")
	if err := s.mgr.Checkpoint(c.Request.Context(), id); err != nil {
		if errors.Is(err, bed.ErrResourcePressure) {
			respondBedError(c, err)
			return
		}
		runtimeError(c, err.Error())
		return
	}
	b, ok := s.mgr.Get(id)
	if !ok {
		runtimeError(c, "checkpointed bed is no longer resident")
		return
	}
	c.JSON(http.StatusOK, gin.H{"persistence": string(b.Spec().Sync)})
}
