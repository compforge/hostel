package handler

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/qiankunli/hostel/internal/api/execd/view"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
)

func (s *Handler) runCommand(c *gin.Context) {
	b, fs, finish := s.opsOf(c)
	if fs == nil {
		return
	}
	defer finish()
	var req view.CommandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, err.Error())
		return
	}
	if req.Command == "" {
		badRequest(c, "missing command")
		return
	}
	if req.UID != nil || req.GID != nil {
		respondError(c, 400, view.ErrNotSupported, "uid/gid overrides are not supported")
		return
	}
	if req.TimeoutMs < 0 || req.TimeoutMs > 86400000 {
		badRequest(c, "timeout must be between 0 and 86400000 milliseconds")
		return
	}
	if err := bed.ValidateRequestEnv(req.Envs); err != nil {
		badRequest(c, err.Error())
		return
	}
	cwd := ""
	if req.Cwd != "" {
		var err error
		cwd, err = s.mgr.Isolator().View(fs).ResolveDirectory(req.Cwd)
		if err != nil {
			badRequest(c, err.Error())
			return
		}
	}
	ctx := c.Request.Context()
	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	stream := &eventStream{c: c}
	if req.Background {
		e, err := s.mgr.StartExecution(context.WithoutCancel(ctx), b, bed.ExecutionBackground, req.Command, cwd, req.Stdin, req.Envs, timeout, nil, nil)
		if err != nil {
			runtimeError(c, err.Error())
			return
		}
		stream.send(view.StreamEvent{Type: "init", Text: e.ID})
		// This acknowledges launch, not the eventual process outcome.
		stream.send(view.StreamEvent{Type: "execution_complete"})
		return
	}
	stop := func() {}
	defer func() { stop() }()
	e, err := s.mgr.StartExecution(ctx, b, bed.ExecutionForeground, req.Command, cwd, req.Stdin, req.Envs, timeout, func(status bed.ExecutionStatus) { stop = stream.start(ctx, status.ID) }, func(out bed.ExecutionOutput) { stream.send(view.StreamEvent{Type: string(out.Stream), Text: out.Text}) })
	if err != nil {
		runtimeError(c, err.Error())
		return
	}
	result := e.Wait()
	stop()
	stop = func() {}
	stream.send(view.Terminal(result))
}
func (s *Handler) sessionCreate(c *gin.Context) {
	b, fs, finish := s.opsOf(c)
	if fs == nil {
		return
	}
	defer finish()
	var req view.SessionCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		badRequest(c, err.Error())
		return
	}
	cwd := ""
	if req.Cwd != "" {
		var err error
		cwd, err = s.mgr.Isolator().View(fs).ResolveDirectory(req.Cwd)
		if err != nil {
			badRequest(c, err.Error())
			return
		}
	}
	id, err := s.mgr.CreateShell(b, cwd)
	if err != nil {
		runtimeError(c, err.Error())
		return
	}
	c.JSON(200, view.SessionCreated{ID: id})
}
func (s *Handler) sessionRun(c *gin.Context) {
	b, fs, finish := s.opsOf(c)
	if fs == nil {
		return
	}
	defer finish()
	sh, ok := b.GetShell(c.Param("sessionId"))
	if !ok || sh.Dead() {
		respondError(c, 404, view.ErrSessionNotFound, "session not found")
		return
	}
	var req view.SessionRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, err.Error())
		return
	}
	if req.Command == "" || req.Timeout < 0 || req.Timeout > 86400000 {
		badRequest(c, "command and valid millisecond timeout required")
		return
	}
	stream := &eventStream{c: c}
	stop := func() {}
	defer func() { stop() }()
	e, err := s.mgr.StartConfiguredSessionExecution(c.Request.Context(), b, sh, req.Command, bed.SessionSettings{Directory: req.Cwd}, time.Duration(req.Timeout)*time.Millisecond, func(status bed.ExecutionStatus) { stop = stream.start(c.Request.Context(), status.ID) }, func(out bed.ExecutionOutput) { stream.send(view.StreamEvent{Type: string(out.Stream), Text: out.Text}) })
	if err != nil {
		respondBedError(c, err)
		return
	}
	result := e.Wait()
	stop()
	stop = func() {}
	stream.send(view.Terminal(result))
}
func (s *Handler) sessionDelete(c *gin.Context, b *bed.Resident) {
	if !b.DeleteShell(c.Param("sessionId")) {
		respondError(c, 404, view.ErrSessionNotFound, "session not found")
		return
	}
	c.Status(200)
}
