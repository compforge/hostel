package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestBedStoreAPISelectionAndConflict(t *testing.T) {
	s := newTestServer(t)
	headers := map[string]string{"Content-Type": "application/json"}
	rec := do(t, s, http.MethodPost, "/v1/beds", strings.NewReader(`{"id":"one","store":"noop"}`), headers)
	if rec.Code != 202 || !strings.Contains(rec.Body.String(), `"store":"noop"`) {
		t.Fatalf("create=%d %s", rec.Code, rec.Body.String())
	}
	rec = waitBedReady(t, s, "one")
	if !strings.Contains(rec.Body.String(), `"store":"noop"`) {
		t.Fatalf("bed=%s", rec.Body.String())
	}
	rec = do(t, s, http.MethodPost, "/v1/beds", strings.NewReader(`{"id":"one","store":"default"}`), headers)
	if rec.Code != 200 {
		t.Fatalf("conflict=%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s, http.MethodPost, "/v1/beds/one/checkpoint", nil, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"persistence":"noop"`) {
		t.Fatalf("checkpoint=%d %s", rec.Code, rec.Body.String())
	}
	for _, payload := range []string{`{"id":"bad","store":"invalid"}`, `{"id":"bad","store":12}`} {
		rec = do(t, s, http.MethodPost, "/v1/beds", strings.NewReader(payload), headers)
		if rec.Code != 400 {
			t.Fatalf("invalid store=%d %s", rec.Code, rec.Body.String())
		}
	}
	rec = do(t, s, http.MethodGet, "/v1/beds/capabilities", nil, nil)
	if !strings.Contains(rec.Body.String(), `"bed_store_selection":true`) {
		t.Fatalf("capabilities=%s", rec.Body.String())
	}
}
