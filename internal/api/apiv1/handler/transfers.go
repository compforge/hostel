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

package handler

import (
	"errors"
	"net/http"
	"time"

	apiview "github.com/qiankunli/hostel/internal/api/apiv1/view"

	"github.com/gin-gonic/gin"
	"github.com/qiankunli/hostel/internal/bed/store"
)

type transferEndpoint struct {
	Ref  string `json:"ref,omitempty"`
	Type string `json:"type"`
	Path string `json:"path,omitempty"`
	Key  string `json:"key,omitempty"`
}

func (e transferEndpoint) endpoint() store.TransferEndpoint {
	return store.TransferEndpoint{Ref: e.Ref, Type: e.Type, Path: e.Path, Key: e.Key}
}

type transferRequest struct {
	ParentRef   string           `json:"parent_ref,omitempty"`
	Sync        store.SyncKind   `json:"sync,omitempty"`
	ID          string           `json:"id"`
	InstanceID  string           `json:"instance_id,omitempty"`
	Source      transferEndpoint `json:"source"`
	Destination transferEndpoint `json:"destination"`
	Overwrite   bool             `json:"overwrite,omitempty"`
	TimeoutMs   int64            `json:"timeout_ms,omitempty"`
}

func (s *Handler) startTransfer(c *gin.Context) {
	c.Header("X-Hostel-Transfer-Instance", s.mgr.TransferInstanceID())
	var req transferRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "invalid transfer request")
		return
	}
	if req.TimeoutMs < 0 || req.TimeoutMs > store.MaxTransferTimeout.Milliseconds() {
		badRequest(c, "timeout_ms must be between 0 and 7200000")
		return
	}
	result, err := s.mgr.StartTransfer(c.Request.Context(), c.Param("bedId"), store.TransferRequest{
		ParentRef: req.ParentRef, Sync: req.Sync, ID: req.ID, InstanceID: req.InstanceID, Source: req.Source.endpoint(), Destination: req.Destination.endpoint(), Overwrite: req.Overwrite, Timeout: time.Duration(req.TimeoutMs) * time.Millisecond,
	})
	if err != nil {
		transferError(c, err)
		return
	}
	code := http.StatusAccepted
	if result.State != store.TransferRunning {
		code = http.StatusOK
	}
	c.JSON(code, apiview.TransferFrom(result))
}

func (s *Handler) transferStatus(c *gin.Context) {
	c.Header("X-Hostel-Transfer-Instance", s.mgr.TransferInstanceID())
	result, err := s.mgr.TransferStatus(c.Param("bedId"), c.Param("transferId"))
	if err != nil {
		transferError(c, err)
		return
	}
	c.JSON(http.StatusOK, apiview.TransferFrom(result))
}

func (s *Handler) cancelTransfer(c *gin.Context) {
	c.Header("X-Hostel-Transfer-Instance", s.mgr.TransferInstanceID())
	result, err := s.mgr.CancelTransfer(c.Param("bedId"), c.Param("transferId"), c.Query("instance_id"))
	if err != nil {
		transferError(c, err)
		return
	}
	code := http.StatusAccepted
	if result.State != store.TransferRunning {
		code = http.StatusOK
	}
	c.JSON(code, apiview.TransferFrom(result))
}

func transferError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrTransferInvalid):
		badRequest(c, err.Error())
	case errors.Is(err, store.ErrTransferConflict):
		respondError(c, http.StatusConflict, "TRANSFER_CONFLICT", err.Error())
	case errors.Is(err, store.ErrTransferNotFound):
		respondError(c, http.StatusNotFound, "TRANSFER_UNKNOWN", err.Error())
	case errors.Is(err, store.ErrTransferCapacity):
		respondError(c, http.StatusTooManyRequests, "TRANSFER_CAPACITY", err.Error())
	case errors.Is(err, store.ErrTransferUnavailable):
		respondError(c, http.StatusServiceUnavailable, apiview.ErrServiceUnavailable, "S3 transfer unavailable")
	default:
		respondBedError(c, err)
	}
}
