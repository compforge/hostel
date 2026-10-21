package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qiankunli/hostel/protocol"

	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

func TestPillowManagementRoutesAndDeclarationConflict(t *testing.T) {
	root := t.TempDir()
	host := hostfacts.Collect()
	m, err := bed.NewManager(host, root, "/bin/sh", isolation.New(host, "shared", root), nil, 0, nil, bed.WithPillows(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(context.Background())
	s := NewServer(m)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	w := request(http.MethodPost, "/v1/beds", `{"id":"pillow-bed","pillows":[{"name":"worker","command":["/bin/sh","-c","sleep 60"],"required":true}]}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("create %d: %s", w.Code, w.Body)
	}
	b, err := declareTestBed(m, t.Context(), "pillow-bed")
	if err != nil {
		t.Fatal(err)
	}
	before := b.Status().LastActiveAt
	for _, path := range []string{"/v1/beds/pillow-bed/pillows", "/v1/beds/pillow-bed/pillows/worker", "/v1/beds/pillow-bed/pillows/worker/logs"} {
		w = request(http.MethodGet, path, "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
		}
	}
	w = request(http.MethodGet, "/v1/beds/pillow-bed/pillows/worker", "")
	var status protocol.PillowStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || status.Phase != "running" || !status.Ready {
		t.Fatalf("pillow lifecycle/readiness contract: %s %v", w.Body, err)
	}
	w = request(http.MethodGet, "/v1/beds/pillow-bed", "")
	var detail protocol.BedDetail
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || detail.Name != "pillow-bed" || detail.ID == "" || detail.ID.String() == detail.Name || !detail.Status.Lifecycle.Readiness.Ready || len(detail.Status.Pillows) != 1 {
		t.Fatalf("public Bed detail contract: %d %s", w.Code, w.Body)
	}
	observed := detail.Status.Pillows[0]
	if observed.Name != "worker" || !observed.Ready || observed.ExecutionID == "" || observed.ExecutionID != status.ExecutionID {
		t.Fatalf("public pillow observation = %+v, endpoint = %+v", observed, status)
	}
	if !b.Status().LastActiveAt.Equal(before) {
		t.Fatal("pillow diagnostics renewed activity")
	}
	w = request(http.MethodPost, "/v1/beds", `{"id":"pillow-bed","pillows":[{"name":"main","mounts":[{"volume":"workspace","path":"/workspace"}]}]}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("declaration conflict: %d %s", w.Code, w.Body)
	}
	w = request(http.MethodPost, "/v1/beds/pillow-bed/pillows/worker/access", `{"hold_seconds":10}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("non-HTTP access: %d %s", w.Code, w.Body)
	}
	w = request(http.MethodGet, "/v1/beds/pillow-bed/pillows/missing", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing pillow: %d", w.Code)
	}
}

// +case:id=pillow_default_http,desc=`Declare two environment-only Pillows in nonalphabetical order`,expect=`command and relative file APIs use the first Pillow; ambiguous and legacy declarations are rejected`
// +link:doc=docs/kernel.md
func TestPillowDefaultHTTPContract(t *testing.T) {
	s := newTestServer(t)
	for _, body := range []string{
		`{"id":"invalid","pillows":null}`,
		`{"id":"invalid","pillows":[]}`,
		`{"id":"invalid","services":[],"pillows":[{"name":"main","mounts":[{"volume":"workspace","path":"/workspace"}]}]}`,
		`{"id":"invalid","env":{"VALUE":"legacy"},"pillows":[{"name":"main","mounts":[{"volume":"workspace","path":"/workspace"}]}]}`,
		`{"id":"invalid","mounts":[],"pillows":[{"name":"main","mounts":[{"volume":"workspace","path":"/workspace"}]}]}`,
		`{"id":"invalid","pillows":[{"name":"main","process":{}}]}`,
		`{"id":"invalid","pillows":[{"name":"main","command":[]}]}`,
	} {
		w := do(t, s, http.MethodPost, "/v1/beds", bytes.NewBufferString(body), nil)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid declaration accepted: %s: %d %s", body, w.Code, w.Body)
		}
	}
	w := do(t, s, http.MethodPost, "/v1/beds", bytes.NewBufferString(`{"id":"ordered","pillows":[{"name":"z-first","workdir":"/tasks","env":{"VALUE":"first"}},{"name":"a-second","env":{"VALUE":"second"}}]}`), nil)
	if w.Code != http.StatusAccepted && w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	waitBedReady(t, s, "ordered")
	w = do(t, s, http.MethodGet, "/v1/beds/ordered", nil, nil)
	var detail protocol.BedDetail
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Workdir != "/tasks" || len(detail.Status.Pillows) != 2 {
		t.Fatalf("default declaration: %+v", detail)
	}
	for _, p := range detail.Status.Pillows {
		if !p.Prepared || p.ExecutionID != "" {
			t.Fatalf("environment-only pillow: %+v", p)
		}
	}
	headers := map[string]string{"X-Hostel-Bed": "ordered"}
	w = do(t, s, http.MethodPost, "/command", bytes.NewBufferString(`{"command":"printf '%s' \"$VALUE\" > output"}`), headers)
	if w.Code != http.StatusOK {
		t.Fatalf("command: %d %s", w.Code, w.Body)
	}
	for _, path := range []string{"output", "/tasks/output"} {
		w = do(t, s, http.MethodGet, "/files/download?path="+path, nil, headers)
		if w.Code != http.StatusOK || w.Body.String() != "first" {
			t.Fatalf("file %s: %d %q", path, w.Code, w.Body.String())
		}
	}
	w = do(t, s, http.MethodPost, "/v1/beds/ordered/pillows/z-first/process/restart", nil, nil)
	if w.Code == http.StatusOK || w.Code == http.StatusAccepted {
		t.Fatal("environment-only Pillow acquired a main process")
	}
}
