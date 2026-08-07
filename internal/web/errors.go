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
	"net/http"

	"github.com/gin-gonic/gin"
)

// ErrorCode mirrors the OpenSandbox execd error vocabulary so SDK error
// handling works against hostel unchanged.
type ErrorCode string

const (
	ErrInvalidRequest     ErrorCode = "INVALID_REQUEST_BODY"
	ErrMissingQuery       ErrorCode = "MISSING_QUERY"
	ErrRuntimeError       ErrorCode = "RUNTIME_ERROR"
	ErrFileNotFound       ErrorCode = "FILE_NOT_FOUND"
	ErrNotSupported       ErrorCode = "NOT_SUPPORTED"
	ErrSessionNotFound    ErrorCode = "SESSION_NOT_FOUND"
	ErrCommandNotFound    ErrorCode = "COMMAND_NOT_FOUND"
	ErrBedInvalid         ErrorCode = "BED_INVALID"
	ErrBedLimitExceeded   ErrorCode = "BED_LIMIT_EXCEEDED"
	ErrInsufficientBed    ErrorCode = "INSUFFICIENT_BED"
	ErrResourcePressure   ErrorCode = "RESOURCE_PRESSURE"
	ErrBedBusy            ErrorCode = "BED_BUSY"
	ErrServiceUnavailable ErrorCode = "SERVICE_UNAVAILABLE"
)

// ErrorResponse keeps execd's required code/message envelope and adds optional
// hostel scheduler hints that existing OpenSandbox SDKs can ignore.
type ErrorResponse struct {
	Code      ErrorCode           `json:"code,omitempty"`
	Message   string              `json:"message,omitempty"`
	Retryable bool                `json:"retryable,omitempty"`
	Pressure  *BedPressureDetails `json:"pressure,omitempty"`
}

// BedPressureDetails is a hostel extension to the OpenSandbox error envelope.
// Existing SDKs continue to read code/message; schedulers can use the frozen
// capacity snapshot without an extra inventory round trip.
type BedPressureDetails struct {
	PinnedBeds    int64 `json:"pinned_beds"`
	MaxPinnedBeds int   `json:"max_pinned_beds"`
	ResidentBeds  int   `json:"resident_beds"`
	MaxBeds       int   `json:"max_beds"`
}

func respondError(c *gin.Context, status int, code ErrorCode, msg string) {
	c.JSON(status, ErrorResponse{Code: code, Message: msg})
}

func badRequest(c *gin.Context, msg string) {
	respondError(c, http.StatusBadRequest, ErrInvalidRequest, msg)
}

func runtimeError(c *gin.Context, msg string) {
	respondError(c, http.StatusInternalServerError, ErrRuntimeError, msg)
}
