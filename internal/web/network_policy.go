package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/tracing"
)

func (s *Server) networkPolicy(c *gin.Context) {
	b, ok := s.mgr.Get(c.Param("bedId"))
	if !ok {
		respondError(c, http.StatusNotFound, ErrBedInvalid, "bed not found")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), bed.DefaultOperationTimeout)
	defer cancel()
	finish, err := s.mgr.BeginOperation(b, bed.OpControl, bed.DefaultOperationTimeout)
	if err != nil {
		respondBedError(c, err)
		return
	}
	defer finish()
	var mutation network.PolicyMutation
	if c.Request.Method != http.MethodGet {
		raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
		if err != nil {
			var size *http.MaxBytesError
			status := http.StatusBadRequest
			if errors.As(err, &size) {
				status = http.StatusRequestEntityTooLarge
			}
			respondError(c, status, ErrInvalidRequest, "invalid network policy body")
			return
		}
		switch c.Request.Method {
		case http.MethodPost, http.MethodPut:
			mutation.Replace = &network.Policy{DefaultAction: "deny"}
			if strings.TrimSpace(string(raw)) != "" {
				err = json.Unmarshal(raw, mutation.Replace)
			}
		case http.MethodPatch:
			err = json.Unmarshal(raw, &mutation.Merge)
			if err == nil && len(mutation.Merge) == 0 {
				err = network.ErrInvalidPolicy
			}
		case http.MethodDelete:
			err = json.Unmarshal(raw, &mutation.Remove)
			if err == nil && len(mutation.Remove) == 0 {
				err = network.ErrInvalidPolicy
			}
		}
		if err != nil {
			respondError(c, http.StatusBadRequest, ErrInvalidRequest, "invalid network policy body")
			return
		}
	}
	status, err := s.mgr.NetworkPolicy(ctx, b, mutation)
	if err != nil {
		switch {
		case errors.Is(err, network.ErrUnavailable):
			respondError(c, http.StatusServiceUnavailable, ErrServiceUnavailable, "bed network policy is unavailable")
		case errors.Is(err, network.ErrInvalidPolicy):
			respondError(c, http.StatusBadRequest, ErrInvalidRequest, err.Error())
		default:
			tracing.InfoContext(ctx, "network policy update failed", "bed", b.Name, "error", err)
			respondError(c, http.StatusInternalServerError, ErrRuntimeError, "network policy update failed")
		}
		return
	}
	if c.Request.Method != http.MethodGet {
		tracing.InfoContext(ctx, "network policy applied", "bed", b.Name, "mode", status.Mode, "rules", len(status.Policy.Egress))
	}
	c.JSON(http.StatusOK, status)
}
