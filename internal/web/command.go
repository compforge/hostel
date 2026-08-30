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
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bedfs"
)

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

// RunCommandRequest mirrors execd's shape.
type RunCommandRequest struct {
	Command    string            `json:"command"`
	Cwd        string            `json:"cwd,omitempty"`
	Background bool              `json:"background,omitempty"`
	TimeoutMs  int64             `json:"timeout,omitempty"`
	Envs       map[string]string `json:"envs,omitempty"`
}

// resolveCwd maps any BedFS client path to its carrier path, or "" when unset.
// The selected isolation mechanism projects that path into its process view.
// The dir is prepared (EnsureDir, owner-aware) because a fresh bed's workspace starts
// empty and a cd into a missing dir would fail.
// Returns false (after writing an error) on an invalid path.
func (s *Server) resolveCwd(c *gin.Context, fs *bedfs.FS, cwd string) (string, bool) {
	if cwd == "" {
		return "", true
	}
	host, err := fs.Resolve(cwd)
	if err != nil {
		badRequest(c, err.Error())
		return "", false
	}
	if err := fs.EnsureDir(host); err != nil {
		runtimeError(c, "prepare workdir: "+err.Error())
		return "", false
	}
	return host, true
}

// POST /command starts one execution. Foreground streams through its terminal
// event; background returns after execution_start and remains queryable.
//
// +spec=`Every execution emits one stable identity and one typed terminal outcome; stdout and stderr remain distinct.`
// +case:id=command_stream_contract,desc=`Run a non-zero command through the public HTTP stream`,expect=`stdout, stderr, executor identity, and exit code remain observable`
func (s *Server) runCommand(c *gin.Context) {
	b, ops, finishRequest := s.opsOf(c)
	if ops == nil {
		return
	}
	defer finishRequest()
	var req RunCommandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, err.Error())
		return
	}
	if req.Command == "" {
		badRequest(c, "missing 'command'")
		return
	}
	if err := bed.ValidateRequestEnv(req.Envs); err != nil {
		badRequest(c, err.Error())
		return
	}
	cwdInBed, ok := s.resolveCwd(c, ops, req.Cwd)
	if !ok {
		return
	}
	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if req.Background {
		executionCtx := context.WithoutCancel(c.Request.Context())
		execution, err := s.mgr.StartExecution(executionCtx, b, bed.ExecutionBackground, req.Command, cwdInBed, req.Envs, timeout, nil, nil)
		if err != nil {
			runtimeError(c, err.Error())
			return
		}
		sse := newSSE(c)
		sse.send(StreamEvent{Type: EventExecutionStart, ExecutionID: execution.ID})
		return
	}

	// Foreground: fresh, isolated one-shot process (execd parity), streamed live.
	// NOT the shared /session shell — a caller script's set -e / exit / trap must
	// die with its own process, never tear down the bed's stateful shell (that
	// was the "shell: session exited during run" failure on skill batch-sync).
	sse := newSSE(c)
	stopSSE := func() {}
	defer func() { stopSSE() }()
	startedExecutionID := ""
	execution, err := s.mgr.StartExecution(c.Request.Context(), b, bed.ExecutionForeground, req.Command, cwdInBed, req.Envs, timeout, func(status bed.ExecutionStatus) {
		startedExecutionID = status.ID
		stopSSE = sse.start(c.Request.Context(), status.ID, ssePingInterval)
	}, func(output bed.ExecutionOutput) {
		eventType := EventStdout
		if output.Stream == bed.StreamStderr {
			eventType = EventStderr
		}
		sequence := output.Sequence
		sse.send(StreamEvent{Type: eventType, ExecutionID: startedExecutionID, Sequence: &sequence, Text: output.Text})
	})
	if err != nil {
		runtimeError(c, err.Error())
		return
	}
	result := execution.Wait()
	payload := resultPayload(result)
	sse.send(StreamEvent{
		Type:        EventExecutionEnd,
		ExecutionID: result.ExecutionID,
		Result:      &payload,
	})
}

// DELETE /command?id=... — interrupt a (background) command.
//
// +spec=`A background execution remains queryable through status and cursor-based logs until its typed terminal result records the actor that stopped it.`
// +case:id=background_execution_control,desc=`Start a background command, read its typed logs, interrupt it, then poll status`,expect=`ordered stdout/stderr and termination_cause=interrupted remain observable under one execution id`
func (s *Server) interruptCommand(c *gin.Context) {
	id := c.Query("id")
	if id == "" {
		respondError(c, http.StatusBadRequest, ErrMissingQuery, "missing query parameter 'id'")
		return
	}
	execution, ok := s.mgr.Executions().Get(id)
	if !ok {
		respondError(c, http.StatusNotFound, ErrCommandNotFound, "command not found: "+id)
		return
	}
	execution.RequestStop(bed.CauseInterrupted)
	c.Status(http.StatusOK)
}

// GET /command/status/:id
func (s *Server) commandStatus(c *gin.Context) {
	execution, ok := s.mgr.Executions().Get(c.Param("id"))
	if !ok {
		respondError(c, http.StatusNotFound, ErrCommandNotFound, "command not found")
		return
	}
	c.JSON(http.StatusOK, executionStatusPayload(execution.Status()))
}

// GET /command/:id/logs?cursor=N returns ordered, typed output fragments.
func (s *Server) commandLogs(c *gin.Context) {
	id := c.Param("id")
	cursor := int64(-1)
	if q := c.Query("cursor"); q != "" {
		if n, err := strconv.ParseInt(q, 10, 64); err == nil {
			cursor = n
		}
	}
	execution, ok := s.mgr.Executions().Get(id)
	if !ok {
		respondError(c, http.StatusNotFound, ErrCommandNotFound, "execution not found")
		return
	}
	output, next, running, truncated := execution.Logs(cursor)
	payload := make([]executionOutputPayload, 0, len(output))
	for _, item := range output {
		payload = append(payload, outputPayload(item))
	}
	c.JSON(http.StatusOK, gin.H{
		"execution_id": id,
		"output":       payload,
		"next_cursor":  next,
		"running":      running,
		"truncated":    truncated,
	})
}

func executionStatusPayload(status bed.ExecutionStatus) gin.H {
	payload := gin.H{
		"execution_id":     status.ID,
		"bed_id":           status.BedID,
		"mode":             status.Mode,
		"executor_id":      status.ExecutorID,
		"executor_backend": status.ExecutorBackend,
		"running":          status.Running,
		"started_at":       status.StartedAt,
		"finished_at":      status.FinishedAt,
	}
	if status.Result != nil {
		payload["result"] = resultPayload(*status.Result)
	}
	return payload
}

// --- /session: explicit stateful bash sessions ---

type createSessionRequest struct {
	Cwd string `json:"cwd,omitempty"`
}
type runInSessionRequest struct {
	Command string `json:"command"`
	Cwd     string `json:"cwd,omitempty"`
	Timeout int64  `json:"timeout,omitempty"`
}

// POST /session
//
// +spec=`An explicit session owns one persistent shell whose cwd and shell variables survive successful runs until the session is deleted.`
// +case:id=stateful_session_lifecycle,desc=`Create a session, mutate shell state, reuse it, then delete it`,expect=`state persists between runs and the deleted session returns 404`
func (s *Server) sessionCreate(c *gin.Context) {
	b, ops, finishOperation := s.opsOf(c)
	if ops == nil {
		return
	}
	defer finishOperation()
	var req createSessionRequest
	_ = c.ShouldBindJSON(&req)
	cwdInBed, ok := s.resolveCwd(c, ops, req.Cwd)
	if !ok {
		return
	}
	id, err := s.mgr.CreateShell(b, cwdInBed)
	if err != nil {
		runtimeError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"session_id": id})
}

// POST /session/:sessionId/run — SSE stream.
func (s *Server) sessionRun(c *gin.Context) {
	b, ops, finishRequest := s.opsOf(c)
	if ops == nil {
		return
	}
	defer finishRequest()
	sh, ok := b.GetShell(c.Param("sessionId"))
	if !ok {
		respondError(c, http.StatusNotFound, ErrSessionNotFound, "session not found")
		return
	}
	var req runInSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, err.Error())
		return
	}
	if req.Command == "" {
		badRequest(c, "missing 'command'")
		return
	}
	cwdInBed, ok := s.resolveCwd(c, ops, req.Cwd)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.Timeout)*time.Millisecond)
		defer cancel()
	}
	sse := newSSE(c)
	stopSSE := func() {}
	defer func() { stopSSE() }()
	startedExecutionID := ""
	execution, err := s.mgr.StartSessionExecution(ctx, b, sh, req.Command, cwdInBed, time.Duration(req.Timeout)*time.Millisecond, func(status bed.ExecutionStatus) {
		startedExecutionID = status.ID
		stopSSE = sse.start(ctx, status.ID, ssePingInterval)
	}, func(output bed.ExecutionOutput) {
		sequence := output.Sequence
		sse.send(StreamEvent{Type: EventStdout, ExecutionID: startedExecutionID, Sequence: &sequence, Text: output.Text})
	})
	if err != nil {
		respondBedError(c, err)
		return
	}
	result := execution.Wait()
	payload := resultPayload(result)
	sse.send(StreamEvent{Type: EventExecutionEnd, ExecutionID: result.ExecutionID, Result: &payload})
}

// DELETE /session/:sessionId
func (s *Server) sessionDelete(c *gin.Context, b *bed.Bed) {
	if !b.DeleteShell(c.Param("sessionId")) {
		respondError(c, http.StatusNotFound, ErrSessionNotFound, "session not found")
		return
	}
	c.Status(http.StatusOK)
}
