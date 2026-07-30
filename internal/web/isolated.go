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
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/qiankunli/go-stdx/shellx"
	"github.com/qiankunli/go-stdx/uuid"

	"github.com/qiankunli/hostel/internal/bed"
)

type isolatedWorkspaceSpec struct {
	Path string `json:"path"`
	Mode string `json:"mode,omitempty"`
}

type isolatedEnvPassthroughSpec struct {
	Mode string   `json:"mode,omitempty"`
	Keys []string `json:"keys,omitempty"`
}

type isolatedBindMount struct {
	Source   string `json:"source"`
	Dest     string `json:"dest,omitempty"`
	ReadOnly bool   `json:"readonly,omitempty"`
}

type isolatedCreateRequest struct {
	Profile            string                     `json:"profile"`
	Workspace          isolatedWorkspaceSpec      `json:"workspace"`
	ExtraWritable      []string                   `json:"extra_writable,omitempty"`
	Binds              []isolatedBindMount        `json:"binds,omitempty"`
	ShareNet           *bool                      `json:"share_net,omitempty"`
	EnvPassthrough     isolatedEnvPassthroughSpec `json:"env_passthrough,omitempty"`
	UID                *uint32                    `json:"uid,omitempty"`
	GID                *uint32                    `json:"gid,omitempty"`
	UIDMode            string                     `json:"uid_mode,omitempty"`
	IdleTimeoutSeconds int                        `json:"idle_timeout_seconds,omitempty"`
}

type isolatedCreateResponse struct {
	SessionID string    `json:"session_id"`
	CreatedAt time.Time `json:"created_at"`
}

type isolatedRunRequest struct {
	Code           string            `json:"code"`
	Envs           map[string]string `json:"envs,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
}

type isolatedSessionState struct {
	Status               string                 `json:"status"`
	CreatedAt            time.Time              `json:"created_at"`
	LastRunAt            time.Time              `json:"last_run_at"`
	IdleRemainingSeconds *int                   `json:"idle_remaining_seconds,omitempty"`
	Profile              string                 `json:"profile,omitempty"`
	Workspace            *isolatedWorkspaceSpec `json:"workspace,omitempty"`
	ShareNet             *bool                  `json:"share_net,omitempty"`
}

type isolatedSessionSummary struct {
	SessionID            string    `json:"session_id"`
	Status               string    `json:"status"`
	CreatedAt            time.Time `json:"created_at"`
	LastRunAt            time.Time `json:"last_run_at"`
	IdleRemainingSeconds *int      `json:"idle_remaining_seconds,omitempty"`
}

type isolatedListResponse struct {
	Sessions []isolatedSessionSummary `json:"sessions"`
}

// withIsolatedBed resolves only an already-resident, non-default bed. The
// default bed is the native API fallback, not an isolated session.
func (s *Server) withIsolatedBed(next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("sessionId")
		if id == s.mgr.DefaultBedID() {
			respondError(c, http.StatusNotFound, ErrSessionNotFound, "session not found")
			return
		}
		b, ok := s.mgr.Get(id)
		if !ok {
			respondError(c, http.StatusNotFound, ErrSessionNotFound, "session not found")
			return
		}
		c.Set(resolvedBedContextKey, b)
		next(c)
	}
}

// POST /v1/isolated/session. The compatibility identity is deliberately
// one-to-one: an isolated session is a non-default bed, not a second lifecycle
// object.
func (s *Server) isolatedCreate(c *gin.Context) {
	if !s.mgr.Isolator().Available() {
		respondError(c, http.StatusServiceUnavailable, ErrServiceUnavailable, "isolation unavailable")
		return
	}
	var req isolatedCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, err.Error())
		return
	}
	if !validateIsolatedCreate(c, req) {
		return
	}

	b, err := s.mgr.Resolve(uuid.V7())
	if err != nil {
		respondBedError(c, err)
		return
	}
	finish, err := s.mgr.BeginOperation(b, 0)
	if err != nil {
		_ = s.mgr.Purge(b.ID)
		respondBedError(c, err)
		return
	}
	defer finish()
	// OpenSandbox creates the persistent shell as part of session creation.
	// Do the same so a 201 response means the bed is ready to run, not merely
	// that its workspace directory exists.
	if _, err := s.mgr.ForegroundShell(b); err != nil {
		_ = s.mgr.Purge(b.ID)
		runtimeError(c, err.Error())
		return
	}
	c.JSON(http.StatusCreated, isolatedCreateResponse{
		SessionID: b.ID,
		CreatedAt: b.CreatedAt,
	})
}

func validateIsolatedCreate(c *gin.Context, req isolatedCreateRequest) bool {
	switch req.Profile {
	case "", "balanced":
	case "strict":
		respondError(c, http.StatusBadRequest, ErrNotSupported, "profile strict is not supported by the bed adapter")
		return false
	default:
		badRequest(c, fmt.Sprintf("invalid profile %q", req.Profile))
		return false
	}
	if req.Workspace.Path == "" {
		badRequest(c, "workspace.path is required")
		return false
	}
	if req.Workspace.Path != "/workspace" {
		respondError(c, http.StatusBadRequest, ErrNotSupported, "only the bed-owned /workspace is supported")
		return false
	}
	switch req.Workspace.Mode {
	case "", "rw":
	case "overlay", "ro":
		respondError(c, http.StatusBadRequest, ErrNotSupported, "only workspace mode rw is supported")
		return false
	default:
		badRequest(c, fmt.Sprintf("invalid workspace mode %q", req.Workspace.Mode))
		return false
	}
	if len(req.ExtraWritable) > 0 || len(req.Binds) > 0 {
		respondError(c, http.StatusBadRequest, ErrNotSupported, "extra_writable and binds are not supported")
		return false
	}
	if req.ShareNet != nil && !*req.ShareNet {
		respondError(c, http.StatusBadRequest, ErrNotSupported, "private network namespaces are not supported")
		return false
	}
	if req.EnvPassthrough.Mode != "" || len(req.EnvPassthrough.Keys) > 0 {
		respondError(c, http.StatusBadRequest, ErrNotSupported, "env_passthrough is not supported")
		return false
	}
	if req.UID != nil || req.GID != nil || req.UIDMode != "" {
		respondError(c, http.StatusBadRequest, ErrNotSupported, "per-session uid/gid switching is not supported")
		return false
	}
	if req.IdleTimeoutSeconds < 0 {
		badRequest(c, "idle_timeout_seconds must be non-negative")
		return false
	}
	if req.IdleTimeoutSeconds > 0 {
		respondError(c, http.StatusBadRequest, ErrNotSupported, "per-session idle timeout is not supported")
		return false
	}
	return true
}

// GET /v1/isolated/session/:sessionId
func (s *Server) isolatedGet(c *gin.Context) {
	b := s.bedOf(c)
	if b == nil {
		return
	}
	c.JSON(http.StatusOK, isolatedState(b))
}

// GET /v1/isolated/sessions
func (s *Server) isolatedList(c *gin.Context) {
	beds := s.mgr.List()
	sort.Slice(beds, func(i, j int) bool { return beds[i].ID < beds[j].ID })
	items := make([]isolatedSessionSummary, 0, len(beds))
	for _, b := range beds {
		if b.ID == s.mgr.DefaultBedID() {
			continue
		}
		state := isolatedState(b)
		items = append(items, isolatedSessionSummary{
			SessionID:            b.ID,
			Status:               state.Status,
			CreatedAt:            state.CreatedAt,
			LastRunAt:            state.LastRunAt,
			IdleRemainingSeconds: state.IdleRemainingSeconds,
		})
	}
	c.JSON(http.StatusOK, isolatedListResponse{Sessions: items})
}

func isolatedState(b *bed.Bed) isolatedSessionState {
	snapshot := b.Snapshot()
	status := "active"
	if snapshot.State == bed.StateEvicting {
		status = "dead"
	}
	var remaining *int
	if !snapshot.ExpiresAt.IsZero() {
		seconds := max(0, int(time.Until(snapshot.ExpiresAt).Seconds()))
		remaining = &seconds
	}
	shareNet := true
	return isolatedSessionState{
		Status:               status,
		CreatedAt:            b.CreatedAt,
		LastRunAt:            snapshot.LastActiveAt,
		IdleRemainingSeconds: remaining,
		Profile:              "balanced",
		Workspace:            &isolatedWorkspaceSpec{Path: "/workspace", Mode: "rw"},
		ShareNet:             &shareNet,
	}
}

// POST /v1/isolated/session/:sessionId/run
func (s *Server) isolatedRun(c *gin.Context) {
	b := s.bedOf(c)
	if b == nil {
		return
	}
	var req isolatedRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, err.Error())
		return
	}
	if req.Code == "" {
		badRequest(c, "missing 'code'")
		return
	}
	if req.TimeoutSeconds < 0 {
		badRequest(c, "timeout_seconds must be non-negative")
		return
	}

	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	finish, err := s.mgr.BeginOperation(b, timeout)
	if err != nil {
		respondBedError(c, err)
		return
	}
	defer finish()
	sh, err := s.mgr.ForegroundShell(b)
	if err != nil {
		runtimeError(c, err.Error())
		return
	}

	ctx := c.Request.Context()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	sse := newSSE(c)
	start := time.Now()
	result, err := sh.Run(ctx, isolatedRunScript(req.Code, req.Envs), func(line string) {
		sse.send(StreamEvent{Type: EventStdout, Text: line})
	})
	b.RecordCommand(time.Since(start))
	if err != nil {
		// Shell.Run stops consuming output when its context ends. Kill that
		// shell so stale output cannot be consumed by the next serialized run.
		if ctx.Err() != nil {
			sh.Close()
		}
		sse.send(StreamEvent{Type: EventError, Error: err.Error()})
		return
	}
	if result.ExitCode != 0 {
		sse.send(StreamEvent{
			Type:     EventError,
			Error:    fmt.Sprintf("command exited with code %d", result.ExitCode),
			ExitCode: &result.ExitCode,
		})
		return
	}
	sse.send(StreamEvent{
		Type:          EventComplete,
		ExecutionTime: time.Since(start).Milliseconds(),
		ExitCode:      &result.ExitCode,
	})
}

// isolatedRunScript matches OpenSandbox's state contract: normal code runs in
// the persistent shell, while request-scoped envs are confined to a subshell.
func isolatedRunScript(code string, envs map[string]string) string {
	if len(envs) == 0 {
		return code
	}
	keys := make([]string, 0, len(envs))
	for key := range envs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var script strings.Builder
	script.WriteString("(\n")
	for _, key := range keys {
		script.WriteString("export ")
		script.WriteString(shellx.Quote(key))
		script.WriteByte('=')
		script.WriteString(shellx.Quote(envs[key]))
		script.WriteByte('\n')
	}
	script.WriteString(code)
	if !strings.HasSuffix(code, "\n") {
		script.WriteByte('\n')
	}
	script.WriteByte(')')
	return script.String()
}

// DELETE /v1/isolated/session/:sessionId
func (s *Server) isolatedDelete(c *gin.Context) {
	if err := s.mgr.Purge(c.Param("sessionId")); err != nil {
		runtimeError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, nil)
}

func (s *Server) isolatedDiff(c *gin.Context) {
	respondError(c, http.StatusServiceUnavailable, ErrNotSupported, "diff is not supported")
}

func (s *Server) isolatedCommit(c *gin.Context) {
	respondError(c, http.StatusServiceUnavailable, ErrNotSupported, "commit is not supported")
}

// GET /v1/isolated/capabilities
func (s *Server) isolatedCapabilities(c *gin.Context) {
	iso := s.mgr.Isolator()
	c.JSON(http.StatusOK, gin.H{
		"available":         iso.Available(),
		"isolator":          iso.Name(),
		"message":           "Hostel bed adapter: balanced profile with an rw /workspace and shared network",
		"setpriv_available": false,
		"userns_available":  false,
		"commit_supported":  false,
		"diff_supported":    false,
	})
}
