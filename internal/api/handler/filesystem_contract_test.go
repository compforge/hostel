package handler

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestBedWorkdirUsesBedPath(t *testing.T) {
	s := newTestServer(t)
	for _, name := range []string{"one", "two"} {
		b, err := s.mgr.Ensure(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		if b.Workdir() == "/workspace" {
			t.Fatal("test requires a distinct carrier workdir")
		}
		rec := do(t, s, http.MethodGet, "/v1/beds/"+name, nil, nil)
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusOK || body["workdir"] != "/workspace" {
			t.Fatalf("bed %s = %d %v", name, rec.Code, body)
		}
		if _, old := body["workspace"]; old {
			t.Fatal("legacy workspace in Bed response")
		}
	}
}

func TestProcessViewHTTPContract(t *testing.T) {
	s := newTestServer(t)
	for _, endpoint := range []string{"/healthz", "/v1/beds/capabilities"} {
		rec := do(t, s, http.MethodGet, endpoint, nil, nil)
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		view, ok := body["process_view"].(map[string]any)
		if rec.Code != http.StatusOK || !ok || view["mode"] == "" {
			t.Fatalf("%s = %d %v", endpoint, rec.Code, body)
		}
		if _, ok := view["rootfs"].(bool); !ok {
			t.Fatalf("%s omits native root capability: %v", endpoint, view)
		}
		for _, removed := range []string{"workspace_mount", "workspace_view"} {
			if _, old := body[removed]; old {
				t.Fatalf("%s still exposes %s", endpoint, removed)
			}
		}
	}
}
