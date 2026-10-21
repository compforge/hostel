package handler

import (
	"net/http"
	"strconv"
	"time"

	apiview "github.com/qiankunli/hostel/internal/api/apiv1/view"

	"github.com/gin-gonic/gin"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
	"github.com/qiankunli/hostel/internal/bed/pillow"
)

func (s *Handler) pillowBed(c *gin.Context) *bed.Resident {
	b, ok := s.mgr.Get(c.Param("bedId"))
	if !ok {
		respondError(c, http.StatusNotFound, apiview.ErrBedInvalid, "resident bed not found")
		return nil
	}
	return b
}
func (s *Handler) pillowList(c *gin.Context) {
	// Include initialization failures; diagnostics never Ensure or renew TTL.
	b := s.mgr.BedModel(c.Param("bedId"))
	if b == nil {
		respondError(c, http.StatusNotFound, apiview.ErrBedInvalid, "bed not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"pillows": s.mgr.Pillows().Status(b)})
}
func (s *Handler) selectedPillow(c *gin.Context) (*bed.Resident, *pillow.Status) {
	b := s.pillowBed(c)
	if b == nil {
		return nil, nil
	}
	for _, status := range s.mgr.Pillows().Status(b.Bed) {
		if status.Name == c.Param("pillow") {
			return b, &status
		}
	}
	respondError(c, http.StatusNotFound, apiview.ErrServiceUnavailable, "pillow not found")
	return nil, nil
}
func (s *Handler) pillowGet(c *gin.Context) {
	_, status := s.selectedPillow(c)
	if status != nil {
		c.JSON(http.StatusOK, status)
	}
}
func (s *Handler) pillowRestart(c *gin.Context) {
	b, _ := s.selectedPillow(c)
	if b == nil {
		return
	}
	finish, err := s.mgr.BeginOperation(b, bed.OpControl, time.Minute)
	if err != nil {
		respondBedError(c, err)
		return
	}
	defer finish()
	if err := s.mgr.Pillows().Restart(b.Bed, c.Param("pillow")); err != nil {
		respondError(c, http.StatusConflict, apiview.ErrServiceUnavailable, err.Error())
		return
	}
	c.Status(http.StatusAccepted)
}
func (s *Handler) pillowAccess(c *gin.Context) {
	b, _ := s.selectedPillow(c)
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
	access, err := s.mgr.Pillows().Access(b.Bed, c.Param("pillow"))
	if err != nil {
		respondError(c, http.StatusServiceUnavailable, apiview.ErrServiceUnavailable, err.Error())
		return
	}
	hold, err := s.mgr.HoldPillow(b, time.Duration(req.HoldSeconds)*time.Second)
	if err != nil {
		respondBedError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"access": access, "bed_id": b.ID, "hold": hold})
}

func (s *Handler) pillowHoldRelease(c *gin.Context) {
	b := s.pillowBed(c)
	if b == nil {
		return
	}
	s.mgr.ReleasePillowHold(b, c.Param("holdId"))
	c.Status(http.StatusNoContent)
}
func (s *Handler) pillowLogs(c *gin.Context) {
	b, status := s.selectedPillow(c)
	if b == nil {
		return
	}
	cursor, err := strconv.ParseInt(c.DefaultQuery("cursor", "0"), 10, 64)
	if err != nil || cursor < 0 {
		badRequest(c, "invalid cursor")
		return
	}
	output, next, running, truncated, err := s.mgr.PillowLogs(b, status.ExecutionID, cursor)
	if err != nil {
		respondError(c, http.StatusNotFound, apiview.ErrCommandNotFound, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"output": output, "next_cursor": next, "running": running, "truncated": truncated})
}
