package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	native "github.com/qiankunli/hostel/internal/api/apiv1/handler"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

type testServer struct {
	*Handler
	engine *gin.Engine
}

func NewServer(mgr *bed.Manager) *testServer {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	h := New(mgr, Config{})
	h.RegisterRoutes(engine)
	native.New(mgr, native.Config{}).RegisterRoutes(engine)
	return &testServer{h, engine}
}
func (s *testServer) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.engine.ServeHTTP(w, r) }
func newTestServer(t *testing.T) *testServer {
	t.Helper()
	root := t.TempDir()
	mgr, err := bed.NewManager(hostfacts.Collect(), root, "default", "/bin/bash", isolation.New(hostfacts.Collect(), "shared", root), nil, 0, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := mgr.Close(ctx); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	return NewServer(mgr)
}

func do(t *testing.T, s *testServer, method, path string, body io.Reader, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func createBedAndWait(t *testing.T, s *testServer, id string) *httptest.ResponseRecorder {
	t.Helper()
	rec := do(t, s, http.MethodPost, "/v1/beds", strings.NewReader(`{"id":"`+id+`"}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusOK {
		t.Fatalf("create bed %s = %d %s", id, rec.Code, rec.Body.String())
	}
	return waitBedReady(t, s, id)
}

func waitBedReady(t *testing.T, s *testServer, id string) *httptest.ResponseRecorder {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		rec := do(t, s, http.MethodGet, "/v1/beds/"+id, nil, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("get bed %s = %d %s", id, rec.Code, rec.Body.String())
		}
		var view struct {
			Status struct {
				Lifecycle struct {
					Readiness struct {
						Ready bool `json:"status"`
					} `json:"readiness"`
				} `json:"lifecycle"`
			} `json:"status"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
			t.Fatalf("decode bed %s: %v", id, err)
		}
		if view.Status.Lifecycle.Readiness.Ready {
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("bed %s did not become ready: %s", id, rec.Body.String())
		}
		time.Sleep(time.Millisecond)
	}
}
