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
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/qiankunli/go-stdx/randx"

	"github.com/qiankunli/hostel/internal/bed"
)

// bedView is the JSON shape for a bed in the management API.
type bedView struct {
	ID           string    `json:"id"`
	State        bed.State `json:"state"` // active | idle | evicting
	DataSynced   bool      `json:"data_synced"`
	Pinned       bool      `json:"pinned"`
	Workspace    string    `json:"workspace"`
	CreatedAt    time.Time `json:"created_at"`
	LastActiveAt time.Time `json:"last_active_at"`
	RetainUntil  time.Time `json:"retained_until,omitzero"`
}

func (s *Server) viewOf(b *bed.Bed) bedView {
	return s.viewFromStatus(b, b.Status())
}

func (s *Server) viewFromStatus(b *bed.Bed, status bed.Status) bedView {
	return bedView{
		ID:           b.ID,
		State:        status.State,
		DataSynced:   status.DataSynced,
		Pinned:       status.Pinned,
		Workspace:    b.Workspace,
		CreatedAt:    b.CreatedAt,
		LastActiveAt: status.LastActiveAt,
		RetainUntil:  status.RetainUntil,
	}
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
	LastActivation *lifecycleRecordView `json:"last_activation,omitempty"`
	LastPersist    *lifecycleRecordView `json:"last_persist,omitempty"`
}

// activityView is what the bed is doing right now, by request category
// (docs/lifecycle.md): operations are in-flight stateless requests, sessions
// are open stateful holds. Sessions never raise the bed's state — an idle bed
// may still hold cdp connections.
type activityView struct {
	Operations map[bed.OperationKind]int `json:"operations,omitempty"`
	Sessions   map[bed.SessionKind]int   `json:"sessions,omitempty"`
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
}

// instanceStatus is the hostel-layer status (docs/lifecycle.md): the only way
// a hostel says "you may release me". The verdict is computed here so upstream
// reads a conclusion instead of reassembling bed_counts / store / luggage.
type instanceStatus string

const (
	instanceRetained   instanceStatus = "retained"   // a resident bed is within its retention promise
	instanceDraining   instanceStatus = "draining"   // resident beds all expired, eviction in progress
	instanceReleasable instanceStatus = "releasable" // nothing resident; snapshots (if any) are remote
	instancePinned     instanceStatus = "pinned"     // noop store with local beds: this instance is the only copy
)

// statusOfInstance folds the inventory rows and the store backend into the
// hostel-layer status. A zero RetainUntil (no idle TTL configured) counts as
// retained — releasable must never be concluded from unknown retention.
func statusOfInstance(beds []bed.InventoryBed, store string, now time.Time) instanceStatus {
	hasResident, hasDormant, allExpired := false, false, true
	for _, b := range beds {
		if b.State == bed.StateDormant {
			hasDormant = true
			continue
		}
		hasResident = true
		if b.RetainUntil.IsZero() || b.RetainUntil.After(now) {
			allExpired = false
		}
	}
	switch {
	case hasResident && !allExpired:
		return instanceRetained
	case hasResident:
		return instanceDraining
	case store == "noop" && hasDormant:
		return instancePinned
	default:
		return instanceReleasable
	}
}

// GET /v1/beds — the scheduler's one-poll picture: instance capacity plus
// every bed this instance holds (resident active/idle/evicting, dormant as
// luggage on disk) with its last persisted generation. Everything here is a
// stale-tolerant hint — freshness is re-enforced at activation, so routing on
// outdated data is slow, never wrong. Callers must treat store "noop" as
// "beds are pinned here": no snapshot exists elsewhere to migrate from.
func (s *Server) bedList(c *gin.Context) {
	beds := s.mgr.Inventory()
	hasBeds := false
	counts := map[string]int{
		string(bed.StateActive):   0,
		string(bed.StateIdle):     0,
		string(bed.StateEvicting): 0,
		string(bed.StateDormant):  0,
	}
	var luggageBytes int64
	var retainUntil time.Time
	retentionKnown := true
	for _, b := range beds {
		counts[string(b.State)]++
		if b.State == bed.StateDormant {
			luggageBytes += b.LocalBytes
		} else {
			hasBeds = true
			if b.RetainUntil.IsZero() {
				retentionKnown = false
			} else if b.RetainUntil.After(retainUntil) {
				retainUntil = b.RetainUntil
			}
		}
	}
	high, low := s.mgr.LuggageLimits()
	var instanceRetainUntil any
	if hasBeds && retentionKnown {
		instanceRetainUntil = retainUntil
	}
	c.JSON(http.StatusOK, gin.H{
		"instance": gin.H{
			"status":             statusOfInstance(beds, s.mgr.StoreName(), time.Now()),
			"store":              s.mgr.StoreName(),
			"isolation":          s.mgr.Isolator().Level().String(),
			"max_beds":           s.mgr.MaxBeds(),
			"pinned_beds":        s.mgr.PinnedBedCount(),
			"max_pinned_beds":    s.mgr.MaxPinnedBeds(),
			"bed_pressure":       s.mgr.BedPressure(),
			"bed_counts":         counts,
			"retained_until":     instanceRetainUntil,
			"luggage_bytes":      luggageBytes,
			"luggage_high_bytes": high,
			"luggage_low_bytes":  low,
			"resource_admission": resourceAdmissionView(s.mgr.ResourceAdmissionReport()),
		},
		"beds": beds,
	})
}

type createBedRequest struct {
	ID string `json:"id,omitempty"`
}

// POST /v1/beds — create (or return existing) a bed. Empty id → server-assigned.
func (s *Server) bedCreate(c *gin.Context) {
	var req createBedRequest
	_ = c.ShouldBindJSON(&req)
	id := req.ID
	if id == "" {
		id = "bed-" + randx.Hex(6)
	}
	b, err := s.mgr.Ensure(id)
	if err != nil {
		respondBedError(c, err)
		return
	}
	c.JSON(http.StatusOK, s.viewOf(b))
}

// GET /v1/beds/:bedId
func (s *Server) bedGet(c *gin.Context) {
	b, ok := s.mgr.Get(c.Param("bedId"))
	if !ok {
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
			LastActivation: lifecycleRecordToView(lifecycle.LastActivation),
			LastPersist:    lifecycleRecordToView(lifecycle.LastPersist),
		},
	})
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
func (s *Server) bedDelete(c *gin.Context) {
	id := c.Param("bedId")
	if c.Query("purge") == "true" {
		if err := s.mgr.Purge(id); err != nil {
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
	evicted, err := s.mgr.Evict(id)
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
		"workspace_mount": iso.MountPoint() != "",
		"max_beds":        s.mgr.MaxBeds(),
		"max_pinned_beds": s.mgr.MaxPinnedBeds(),
		"persistence":     s.mgr.StoreName(),
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
		if errors.Is(err, bed.ErrInsufficientBed) {
			respondBedError(c, err)
			return
		}
		runtimeError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"persistence": s.mgr.StoreName()})
}
