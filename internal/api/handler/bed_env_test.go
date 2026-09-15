package handler

import (
	"net/http"
	"strings"
	"testing"
)

func TestCreateBedEnvironmentContract(t *testing.T) {
	s := newTestServer(t)
	body := `{"id":"env-bed","env":{"VALUE":"private-base-value"}}`
	rec := do(t, s, http.MethodPost, "/v1/beds", strings.NewReader(body), nil)
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "private-base-value") {
		t.Fatal("create response exposed environment")
	}
	waitBedReady(t, s, "env-bed")
	for _, path := range []string{"/v1/beds", "/v1/beds/env-bed"} {
		rec = do(t, s, http.MethodGet, path, nil, nil)
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "private-base-value") {
			t.Fatalf("status exposed environment or failed: %s %d", path, rec.Code)
		}
	}
	b, ok := s.mgr.Get("env-bed")
	if !ok || b.Spec().Env["VALUE"] != "private-base-value" {
		t.Fatal("create did not forward environment to Bed")
	}
	rec = do(t, s, http.MethodPost, "/v1/beds", strings.NewReader(body), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("idempotent create: %d", rec.Code)
	}
	for _, body := range []string{`{"id":"env-bed"}`, `{"id":"env-bed","env":{}}`, `{"id":"env-bed","env":{"VALUE":"different"}}`} {
		rec = do(t, s, http.MethodPost, "/v1/beds", strings.NewReader(body), nil)
		if rec.Code != http.StatusConflict || strings.Contains(rec.Body.String(), "private-base-value") || strings.Contains(rec.Body.String(), "different") {
			t.Fatalf("conflicting create: %d %s", rec.Code, rec.Body.String())
		}
	}
	for _, env := range []string{`{"BED_ID":"bad"}`, `{"HOSTEL_SECRET":"bad"}`, `{"PLAYWRIGHT_MCP_CDP_ENDPOINT":"bad"}`, `{"bad-name":"bad"}`, `{"VALUE":"bad\u0000value"}`, `{"VALUE":123}`} {
		rec = do(t, s, http.MethodPost, "/v1/beds", strings.NewReader(`{"id":"invalid","env":`+env+`}`), nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid environment: %d %s", rec.Code, rec.Body.String())
		}
	}
}
