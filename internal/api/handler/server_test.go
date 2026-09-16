package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
)

// testServer exercises the production route registration with direct access to
// handler dependencies. API bootstrap and middleware have separate tests.
type testServer struct {
	*Handler
	engine *gin.Engine
}

func NewServer(mgr *bed.Manager, options ...func(*Config)) *testServer {
	cfg := Config{}
	for _, option := range options {
		option(&cfg)
	}
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.HandleMethodNotAllowed = true
	engine.Use(gin.Recovery())
	h := New(mgr, cfg)
	h.RegisterRoutes(engine)
	return &testServer{Handler: h, engine: engine}
}

func WithDormReadFallbackRoot(root string) func(*Config) {
	return func(cfg *Config) { cfg.DormReadFallbackRoot = root }
}

func (s *testServer) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.engine.ServeHTTP(w, r) }
