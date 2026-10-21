package handler

import (
	"context"
	"errors"
	"strings"
	"testing"

	hostel "github.com/qiankunli/hostel/internal"
	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/manager"
)

func testWorkspacePillows() []model.PillowSpec {
	return []model.PillowSpec{{Name: "main", Mounts: []model.VolumeMount{{Volume: "workspace", Path: "/workspace"}}}}
}

// declareTestBed supplies the fixture's workload before waiting for readiness.
// Existing declarations are used unchanged by tests that configured their own Pillows.
func declareTestBed(m *manager.Manager, ctx context.Context, name string) (*manager.Resident, error) {
	if b, err := m.Ensure(ctx, name); !errors.Is(err, hostel.ErrNotFound) {
		return b, err
	}
	if _, err := m.InitializeBed(ctx, name, manager.CreateOptions{Pillows: testWorkspacePillows()}); err != nil {
		return nil, err
	}
	return m.Ensure(ctx, name)
}

// +case:id=explicit_bed_declaration,desc=`Create with missing/null/empty Pillows and access an undeclared Bed`,expect=`400/404 without allocating Bed identity or data`
func TestBedDeclarationRequiredHTTP(t *testing.T) {
	s := newTestServer(t)
	for _, body := range []string{`{"id":"missing"}`, `{"id":"missing","pillows":null}`, `{"id":"missing","pillows":[]}`} {
		rec := do(t, s, "POST", "/v1/beds", strings.NewReader(body), nil)
		if rec.Code != 400 {
			t.Fatalf("create %s: %d %s", body, rec.Code, rec.Body)
		}
	}
	for _, request := range []struct{ method, path, body string }{
		{"POST", "/command", `{"command":"true"}`}, {"POST", "/session", `{}`}, {"GET", "/files/info?path=/workspace", ""},
	} {
		rec := do(t, s, request.method, request.path, strings.NewReader(request.body), map[string]string{BedHeader: "missing"})
		if rec.Code != 404 {
			t.Fatalf("%s: %d %s", request.path, rec.Code, rec.Body)
		}
	}
	if _, ok := s.mgr.Get("missing"); ok {
		t.Fatal("data request created Bed")
	}
	if _, ok := s.mgr.Initialization("missing"); ok {
		t.Fatal("data request started initialization")
	}
}
