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
	Workspace    string    `json:"workspace"`
	CreatedAt    time.Time `json:"created_at"`
	LastActiveAt time.Time `json:"last_active_at"`
	ExpiresAt    time.Time `json:"expires_at,omitzero"`
}

func viewOf(b *bed.Bed) bedView {
	return viewFromSnapshot(b, b.Snapshot())
}

func viewFromSnapshot(b *bed.Bed, snapshot bed.Snapshot) bedView {
	return bedView{
		ID:           b.ID,
		State:        snapshot.State,
		Workspace:    b.Workspace,
		CreatedAt:    b.CreatedAt,
		LastActiveAt: snapshot.LastActiveAt,
		ExpiresAt:    snapshot.ExpiresAt,
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

type bedDetailView struct {
	bedView
	Generation       int64          `json:"generation"`
	ActiveOperations int            `json:"active_operations"`
	Lifecycle        *lifecycleView `json:"lifecycle,omitempty"`
}

// GET /v1/beds
func (s *Server) bedList(c *gin.Context) {
	beds := s.mgr.List()
	out := make([]bedView, 0, len(beds))
	for _, b := range beds {
		out = append(out, viewOf(b))
	}
	c.JSON(http.StatusOK, gin.H{"beds": out})
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
	b, err := s.mgr.Resolve(id)
	if err != nil {
		respondBedError(c, err)
		return
	}
	c.JSON(http.StatusOK, viewOf(b))
}

// GET /v1/beds/:bedId
func (s *Server) bedGet(c *gin.Context) {
	b, ok := s.mgr.Get(c.Param("bedId"))
	if !ok {
		respondError(c, http.StatusNotFound, ErrBedInvalid, "bed not found")
		return
	}
	snapshot := b.Snapshot()
	lifecycle := b.Lifecycle()
	c.JSON(http.StatusOK, bedDetailView{
		bedView:          viewFromSnapshot(b, snapshot),
		Generation:       snapshot.Generation,
		ActiveOperations: snapshot.ActiveOperations,
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
		"persistence":     s.mgr.StoreName(),
		"resource_accounting": gin.H{
			"backend":   resources.Backend,
			"available": resources.Available,
			"reason":    resources.Reason,
		},
		"files":       true,
		"directories": true,
		"command":     true,
		"session":     true,
		"beds":        true,
		"inventory":   true,
		"amenities":   amenities, // name → unavailable|idle|running
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
		runtimeError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"persistence": s.mgr.StoreName()})
}
