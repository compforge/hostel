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
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
	"github.com/qiankunli/hostel/internal/tracing"
	"github.com/qiankunli/hostel/pkg/mcpproxy"
)

// mcpRequest participates in normal bed admission and activity tracking.
// These are trusted control-plane APIs, like /command; bed routing is not an
// authentication credential. Do not expose them as an untrusted MCP server.
func (s *Server) mcpRequest(next func(*gin.Context, *mcpproxy.Proxy) error) gin.HandlerFunc {
	return func(c *gin.Context) {
		b := s.bedOf(c)
		if b == nil {
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), bed.DefaultOperationTimeout)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		finish, err := s.mgr.BeginOperation(b, bed.OpMCP, bed.DefaultOperationTimeout)
		if err != nil {
			respondBedError(c, err)
			return
		}
		defer finish()
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
		t, err := s.mgr.Amenities().MCP(ctx, b.ID)
		if err != nil {
			respondError(c, http.StatusServiceUnavailable, ErrServiceUnavailable, err.Error())
			return
		}
		start := time.Now()
		err = next(c, t.Proxy())
		if err != nil {
			respondMCPError(c, err)
		}
		tracing.InfoContext(ctx, "MCP request completed", "bed", b.Name, "route", c.FullPath(), "server", c.Param("name"), "tool", c.GetString("mcp.tool"), "status", c.Writer.Status(), "elapsed", time.Since(start))
	}
}

func (s *Server) mcpConfigure(c *gin.Context, p *mcpproxy.Proxy) error {
	var bundle mcpproxy.Bundle
	if err := c.ShouldBindJSON(&bundle); err != nil {
		return mcpproxy.ErrInvalid
	}
	changed, err := p.Configure(bundle)
	if err != nil {
		return err
	}
	c.JSON(http.StatusOK, gin.H{"revision": bundle.Revision, "servers": len(bundle.Servers), "changed": changed})
	return nil
}
func (s *Server) mcpListTools(c *gin.Context, p *mcpproxy.Proxy) error {
	out, err := p.ListTools(c.Request.Context(), c.Param("name"))
	if err != nil {
		return err
	}
	c.Data(http.StatusOK, "application/json", out)
	return nil
}
func (s *Server) mcpCallTool(c *gin.Context, p *mcpproxy.Proxy) error {
	var call mcpproxy.Call
	if err := c.ShouldBindJSON(&call); err != nil {
		return mcpproxy.ErrInvalid
	}
	c.Set("mcp.tool", call.Name)
	out, err := p.CallTool(c.Request.Context(), c.Param("name"), call)
	if err != nil {
		return err
	}
	c.Data(http.StatusOK, "application/json", out)
	return nil
}
func respondMCPError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, mcpproxy.ErrInvalid):
		respondError(c, http.StatusBadRequest, ErrInvalidRequest, err.Error())
	case errors.Is(err, mcpproxy.ErrNotFound):
		respondError(c, http.StatusNotFound, ErrMCPServerNotFound, err.Error())
	case errors.Is(err, mcpproxy.ErrCapacity):
		respondError(c, http.StatusTooManyRequests, ErrMCPCapacity, err.Error())
	case errors.Is(err, mcpproxy.ErrClosed):
		respondError(c, http.StatusServiceUnavailable, ErrServiceUnavailable, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		respondError(c, http.StatusGatewayTimeout, ErrMCPTimeout, "MCP request timed out")
	case errors.Is(err, context.Canceled):
		respondError(c, http.StatusRequestTimeout, ErrMCPTimeout, "MCP request cancelled")
	default:
		respondError(c, http.StatusBadGateway, ErrMCPUpstream, "remote MCP request failed")
	}
}
