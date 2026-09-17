package handler

import (
	"io"
	"strconv"

	"github.com/gin-gonic/gin"
	apiview "github.com/qiankunli/hostel/internal/api/execd/view"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
)

func (s *Handler) execdExecution(c *gin.Context) (*bed.Execution, bool) {
	b := s.bedOf(c)
	e, ok := s.mgr.Executions().Get(c.Param("id"))
	if !ok || e.BedID != b.Name || e.Mode == bed.ExecutionService {
		respondError(c, 404, apiview.ErrCommandNotFound, "command not found")
		return nil, false
	}
	return e, true
}
func (s *Handler) execdStatus(c *gin.Context) {
	e, ok := s.execdExecution(c)
	if !ok {
		return
	}
	c.JSON(200, apiview.Command(e.Status(), e.Content, e.OutputError()))
}
func (s *Handler) execdLogs(c *gin.Context) {
	e, ok := s.execdExecution(c)
	if !ok {
		return
	}
	if e.Mode != bed.ExecutionBackground {
		badRequest(c, "logs require a background command")
		return
	}
	cursor := int64(0)
	if q, ok := c.GetQuery("cursor"); ok {
		n, err := strconv.ParseInt(q, 10, 64)
		if err != nil || n < 0 {
			badRequest(c, "cursor must be nonnegative")
			return
		}
		cursor = n
	}
	f, next, err := e.OpenBackgroundLog(cursor)
	if err != nil {
		runtimeError(c, err.Error())
		return
	}
	defer f.Close()
	c.Header("EXECD-COMMANDS-TAIL-CURSOR", strconv.FormatInt(next, 10))
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Content-Length", strconv.FormatInt(next-min(cursor, next), 10))
	c.Status(200)
	_, _ = io.CopyN(c.Writer, f, next-min(cursor, next))
}
