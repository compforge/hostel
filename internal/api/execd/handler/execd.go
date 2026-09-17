package handler

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	apiview "github.com/qiankunli/hostel/internal/api/execd/view"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
)

const resolvedBedContextKey = "hostel.execd-bed"

// Both addresses bind one existing Bed before entering exactly the same API
// adapter. Path addressing must never be overridden by caller headers.
func (s *Handler) registerExecd(r *gin.RouterGroup) {
	r.Use(s.bindExecdBed)
	r.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.POST("/command", s.runCommand)
	r.DELETE("/command", s.execdInterrupt)
	r.GET("/files/download", s.execdDownload)
	r.POST("/files/upload", s.execdUpload)
	r.DELETE("/directories", s.execdRemoveDirectories)
	r.GET("/files/info", s.execdInfo)
	r.GET("/files/search", s.execdSearch)
	r.DELETE("/files", s.filesDelete)
	r.POST("/files/mv", s.filesRename)
	r.POST("/files/permissions", s.execdPermissions)
	r.POST("/files/replace", s.filesReplace)
	r.GET("/directories/list", s.execdList)
	r.POST("/directories", s.execdMkdir)
	r.POST("/session", s.sessionCreate)
	r.POST("/session/:sessionId/run", s.sessionRun)
	r.DELETE("/session/:sessionId", s.withOp(bed.OpControl, s.sessionDelete))
	r.GET("/command/status/:id", s.execdStatus)
	r.GET("/command/:id/logs", s.execdLogs)
	for _, route := range []string{"/code", "/code/context", "/code/contexts", "/code/contexts/:contextId"} {
		r.Any(route, func(c *gin.Context) {
			respondError(c, 501, apiview.ErrNotSupported, "code execution kernels are not supported")
		})
	}
}

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
	c.Next()
}

func (s *Handler) execdInterrupt(c *gin.Context) {
	b := s.bedOf(c)
	execution, ok := s.mgr.Executions().Get(c.Query("id"))
	if !ok || execution.BedID != b.Name || execution.Mode == bed.ExecutionService {
		respondError(c, 404, apiview.ErrCommandNotFound, "command not found")
		return
	}
	execution.RequestStop(bed.CauseInterrupted)
	c.Status(200)
}
