package web

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
)

type bedExpirationView struct {
	ExpiresAt *time.Time `json:"expiresAt"`
}

func (s *Server) bedRenewExpiration(c *gin.Context) {
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
		respondError(c, http.StatusNotFound, ErrBedInvalid, "resident bed not found")
		return
	}
	until, err := s.mgr.RenewExpiration(resident, request.ExpiresAt)
	if errors.Is(err, bed.ErrInvalidExpiration) {
		badRequest(c, err.Error())
		return
	}
	if err != nil {
		respondError(c, http.StatusConflict, ErrBedBusy, "bed is no longer resident or is being reclaimed")
		return
	}
	response := bedExpirationView{}
	if !until.IsZero() {
		response.ExpiresAt = &until
	}
	c.JSON(http.StatusOK, response)
}
