package handler

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	apiview "github.com/qiankunli/hostel/internal/api/view"
)

const execdContextKey = "hostel.open-sandbox-execd"

// Both addresses bind one existing Bed before entering exactly the same API
// adapter. Path addressing must never be overridden by caller headers.
func (s *Handler) registerExecd(r *gin.RouterGroup) {
	r.Use(s.bindExecdBed)
	r.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.POST("/command", s.runCommand)
	r.DELETE("/command", s.execdInterrupt)
	r.GET("/files/download", s.execdFile(s.execdDownload))
	r.POST("/files/upload", s.execdFile(s.execdUpload))
	r.DELETE("/directories", s.execdFile(s.execdRemoveDirectories))
}

func (s *Handler) isExecd(c *gin.Context) bool { return c.GetBool(execdContextKey) }

func (s *Handler) bindExecdBed(c *gin.Context) {
	name := c.Param("bedId")
	if name == "" {
		name = c.GetHeader(BedHeader)
	}
	if name == "" {
		name = s.mgr.DefaultBedID()
	}
	b, ok := s.mgr.Get(name)
	if !ok {
		respondError(c, 404, apiview.ErrBedInvalid, "bed not found")
		c.Abort()
		return
	}
	token := b.Spec().Env["EXECD_ACCESS_TOKEN"]
	if token != "" && !(c.Request.Method == http.MethodGet && strings.HasSuffix(c.FullPath(), "/ping")) {
		supplied := c.GetHeader("X-EXECD-ACCESS-TOKEN")
		if supplied == "" {
			supplied = strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(supplied)) != 1 {
			respondError(c, 401, "UNAUTHORIZED", "invalid execd credential")
			c.Abort()
			return
		}
	}
	c.Set(resolvedBedContextKey, b)
	c.Set(execdContextKey, true)
	c.Next()
}

func (s *Handler) execdFile(next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		b := s.bedOf(c)
		if b == nil || !s.prepareExecd(c, b, nil) {
			return
		}
		next(c)
	}
}

func (s *Handler) execdInterrupt(c *gin.Context) {
	b := s.bedOf(c)
	execution, ok := s.mgr.Executions().Get(c.Query("id"))
	if !ok || execution.BedID != b.Name {
		respondError(c, 404, apiview.ErrCommandNotFound, "command not found")
		return
	}
	if !s.prepareExecd(c, b, nil) {
		return
	}
	s.interruptCommand(c)
}
