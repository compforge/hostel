// Package api assembles Hostel's HTTP server, middleware and routes.
package api

import (
	"net/http"
	"slices"

	"github.com/gin-gonic/gin"
	"github.com/qiankunli/hostel/internal/api/handler"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

// Server owns the HTTP engine; domain lifecycles remain with their managers.
type Server struct {
	engine *gin.Engine
}

type serverConfig struct {
	tracing              bool
	dormReadFallbackRoot string
}

type ServerOption func(*serverConfig)

func WithTracing(enabled bool) ServerOption {
	return func(cfg *serverConfig) { cfg.tracing = enabled }
}

// WithDormReadFallbackRoot opts an exclusive dorm carrier into reading its
// process root. Shared carriers must leave it empty to avoid cross-bed access.
func WithDormReadFallbackRoot(root string) ServerOption {
	return func(cfg *serverConfig) { cfg.dormReadFallbackRoot = root }
}

func NewServer(mgr *bed.Manager, options ...ServerOption) *Server {
	cfg := serverConfig{}
	for _, option := range options {
		option(&cfg)
	}
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.HandleMethodNotAllowed = true
	if cfg.tracing {
		engine.Use(otelgin.Middleware("hostel", otelgin.WithFilter(traceHTTPPath)))
	}
	engine.Use(gin.Recovery())
	handlers := handler.New(mgr, handler.Config{DormReadFallbackRoot: cfg.dormReadFallbackRoot})
	handlers.RegisterRoutes(engine)
	return &Server{engine: engine}
}

func traceHTTPPath(request *http.Request) bool {
	return !slices.Contains([]string{"/healthz", "/ping", "/metrics", "/metrics/watch", "/v1/status"}, request.URL.Path)
}

func (s *Server) Handler() http.Handler { return s.engine }
