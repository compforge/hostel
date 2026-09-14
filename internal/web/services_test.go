package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

func TestServiceManagementRoutesAndDeclarationConflict(t *testing.T) {
	root := t.TempDir()
	host := hostfacts.Collect()
	m, err := bed.NewManager(host, root, "default", "/bin/sh", isolation.New(host, "shared", root), nil, 0, nil, bed.WithServices(nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(context.Background())
	s := NewServer(m)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	w := request(http.MethodPost, "/v1/beds", `{"id":"service-bed","services":[{"name":"worker","command":["/bin/sh","-c","sleep 60"],"required":true}]}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("create %d: %s", w.Code, w.Body)
	}
	b, err := m.Ensure(t.Context(), "service-bed")
	if err != nil {
		t.Fatal(err)
	}
	before := b.Status().LastActiveAt
	for _, path := range []string{"/v1/beds/service-bed/services", "/v1/beds/service-bed/services/worker", "/v1/beds/service-bed/services/worker/logs"} {
		w = request(http.MethodGet, path, "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
		}
	}
	w = request(http.MethodGet, "/v1/beds/service-bed/services/worker", "")
	var status struct {
		Phase string `json:"phase"`
		Ready bool   `json:"ready"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || status.Phase != "running" || !status.Ready {
		t.Fatalf("service lifecycle/readiness contract: %s %v", w.Body, err)
	}
	if !b.Status().LastActiveAt.Equal(before) {
		t.Fatal("service diagnostics renewed activity")
	}
	w = request(http.MethodPost, "/v1/beds", `{"id":"service-bed"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("declaration conflict: %d %s", w.Code, w.Body)
	}
	w = request(http.MethodPost, "/v1/beds/service-bed/services/worker/access", `{"hold_seconds":10}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("non-HTTP access: %d %s", w.Code, w.Body)
	}
	w = request(http.MethodGet, "/v1/beds/service-bed/services/missing", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing service: %d", w.Code)
	}
}
