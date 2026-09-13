package web

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
	"github.com/qiankunli/hostel/internal/bed/service"
)

func (s *Server) serviceBed(c *gin.Context) *bed.Resident {
	b, ok := s.mgr.Get(c.Param("bedId"))
	if !ok {
		respondError(c, http.StatusNotFound, ErrBedInvalid, "resident bed not found")
		return nil
	}
	return b
}
func (s *Server) serviceList(c *gin.Context) {
	// Include initialization failures; diagnostics never Ensure or renew TTL.
	b := s.mgr.BedModel(c.Param("bedId"))
	if b == nil {
		respondError(c, http.StatusNotFound, ErrBedInvalid, "bed not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"services": s.mgr.Services().Status(b)})
}
func (s *Server) selectedService(c *gin.Context) (*bed.Resident, *service.Status) {
	b := s.serviceBed(c)
	if b == nil {
		return nil, nil
	}
	for _, status := range s.mgr.Services().Status(b.Bed) {
		if status.Name == c.Param("service") {
			return b, &status
		}
	}
	respondError(c, http.StatusNotFound, ErrServiceUnavailable, "service not found")
	return nil, nil
}
func (s *Server) serviceGet(c *gin.Context) {
	_, status := s.selectedService(c)
	if status != nil {
		c.JSON(http.StatusOK, status)
	}
}
func (s *Server) serviceRestart(c *gin.Context) {
	b, _ := s.selectedService(c)
	if b == nil {
		return
	}
	finish, err := s.mgr.BeginOperation(b, bed.OpControl, time.Minute)
	if err != nil {
		respondBedError(c, err)
		return
	}
	defer finish()
	if err := s.mgr.Services().Restart(b.Bed, c.Param("service")); err != nil {
		respondError(c, http.StatusConflict, ErrServiceUnavailable, err.Error())
		return
	}
	c.Status(http.StatusAccepted)
}
func (s *Server) serviceAccess(c *gin.Context) {
	b, _ := s.selectedService(c)
	if b == nil {
		return
	}
	var req struct {
		HoldSeconds int `json:"hold_seconds"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.HoldSeconds < 1 || req.HoldSeconds > 7200 {
		badRequest(c, "hold_seconds must be between 1 and 7200")
		return
	}
	access, err := s.mgr.Services().Access(b.Bed, c.Param("service"))
	if err != nil {
		respondError(c, http.StatusServiceUnavailable, ErrServiceUnavailable, err.Error())
		return
	}
	hold, err := s.mgr.HoldService(b, time.Duration(req.HoldSeconds)*time.Second)
	if err != nil {
		respondBedError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"access": access, "bed_id": b.ID, "hold": hold})
}

func (s *Server) serviceHoldRelease(c *gin.Context) {
	b := s.serviceBed(c)
	if b == nil {
		return
	}
	s.mgr.ReleaseServiceHold(b, c.Param("holdId"))
	c.Status(http.StatusNoContent)
}
func (s *Server) serviceLogs(c *gin.Context) {
	b, status := s.selectedService(c)
	if b == nil {
		return
	}
	cursor, err := strconv.ParseInt(c.DefaultQuery("cursor", "0"), 10, 64)
	if err != nil || cursor < 0 {
		badRequest(c, "invalid cursor")
		return
	}
	output, next, running, truncated, err := s.mgr.ServiceLogs(b, status.ExecutionID, cursor)
	if err != nil {
		respondError(c, http.StatusNotFound, ErrCommandNotFound, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"output": output, "next_cursor": next, "running": running, "truncated": truncated})
}
