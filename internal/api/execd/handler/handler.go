// Package handler adapts the OpenSandbox Execd protocol to Bed-owned capabilities.
package handler

import (
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	apiview "github.com/qiankunli/hostel/internal/api/execd/view"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
)

const BedHeader = "X-Hostel-Bed"

type Config struct{ DormReadFallbackRoot string }
type Handler struct {
	mgr                  *bed.Manager
	dormReadFallbackRoot string
}

func New(mgr *bed.Manager, cfg Config) *Handler {
	return &Handler{mgr: mgr, dormReadFallbackRoot: cfg.DormReadFallbackRoot}
}
func (s *Handler) RegisterRoutes(engine *gin.Engine) {
	s.registerExecd(engine.Group("/execd"))
	s.registerExecd(engine.Group("/v1/beds/:bedId/execd"))
}
func (s *Handler) bedOf(c *gin.Context) *bed.Resident {
	return c.MustGet(resolvedBedContextKey).(*bed.Resident)
}
func (s *Handler) withOp(kind bed.OperationKind, next func(*gin.Context, *bed.Resident)) gin.HandlerFunc {
	return func(c *gin.Context) {
		b := s.bedOf(c)
		finish, err := s.mgr.BeginOperation(b, kind, 0)
		if err != nil {
			respondBedError(c, err)
			return
		}
		defer finish()
		next(c, b)
	}
}
func (s *Handler) opsOf(c *gin.Context) (*bed.Resident, *bedfs.FS, func()) {
	b := s.bedOf(c)
	finish, err := s.mgr.BeginOperation(b, bed.OpFile, 0)
	if err != nil {
		respondBedError(c, err)
		return nil, nil, nil
	}
	return b, b.BedFS(), finish
}
func (s *Handler) fileReader(fs *bedfs.FS) *bedfs.Reader {
	fallback := ""
	if s.mgr.Isolator().Level() == isolation.Shared {
		fallback = s.dormReadFallbackRoot
	}
	return bedfs.NewReader(s.mgr.Isolator().View(fs), fallback)
}
func respondBedError(c *gin.Context, err error) {
	respondError(c, 409, apiview.ErrServiceUnavailable, err.Error())
}
func downloadErr(c *gin.Context, err error) {
	if os.IsNotExist(err) {
		respondError(c, http.StatusNotFound, apiview.ErrFileNotFound, err.Error())
		return
	}
	runtimeError(c, err.Error())
}
