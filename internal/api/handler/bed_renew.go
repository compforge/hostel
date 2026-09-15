package handler

import (
	"errors"
	"io"
	"net/http"
	"time"

	apiview "github.com/qiankunli/hostel/internal/api/view"

	"github.com/gin-gonic/gin"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
)

func (s *Handler) bedRenewExpiration(c *gin.Context) {
	var request struct {
		ExpiresAt *time.Time `json:"expiresAt"`
	}
	if err := c.ShouldBindJSON(&request); err != nil && !errors.Is(err, io.EOF) {
		badRequest(c, "expiresAt must be a future RFC3339 timestamp")
		return
	}
	// Get, not Ensure: renewal never creates or wakes a Bed.
	resident, ok := s.mgr.Get(c.Param("bedId"))
	if !ok {
		respondError(c, http.StatusNotFound, apiview.ErrBedInvalid, "resident bed not found")
		return
	}
	until, err := s.mgr.RenewExpiration(resident, request.ExpiresAt)
	if errors.Is(err, bed.ErrInvalidExpiration) {
		badRequest(c, err.Error())
		return
	}
	if err != nil {
		respondError(c, http.StatusConflict, apiview.ErrBedBusy, "bed is no longer resident or is being reclaimed")
		return
	}
	response := apiview.BedExpiration{}
	if !until.IsZero() {
		response.ExpiresAt = &until
	}
	c.JSON(http.StatusOK, response)
}
