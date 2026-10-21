package handler

import (
	"net/http"
	"strings"
	"testing"
)

func TestBedStoreAPISelectionAndConflict(t *testing.T) {
	s := newTestServer(t)
	headers := map[string]string{"Content-Type": "application/json"}
	rec := do(t, s, http.MethodPost, "/v1/beds", strings.NewReader(`{"id":"one","volumes":[{"name":"workspace","managed":{"persistence":"none"}}],"pillows":[{"name":"main","mounts":[{"volume":"workspace","path":"/workspace"}]}]}`), headers)
	if rec.Code != 202 || !strings.Contains(rec.Body.String(), `"sync":"noop"`) {
		t.Fatalf("create=%d %s", rec.Code, rec.Body.String())
	}
	rec = waitBedReady(t, s, "one")
	if !strings.Contains(rec.Body.String(), `"sync":"noop"`) {
		t.Fatalf("bed=%s", rec.Body.String())
	}
	rec = do(t, s, http.MethodPost, "/v1/beds", strings.NewReader(`{"id":"one","volumes":[{"name":"workspace","managed":{"persistence":"none"}}],"pillows":[{"name":"main","mounts":[{"volume":"workspace","path":"/workspace"}]}]}`), headers)
	if rec.Code != 200 {
		t.Fatalf("conflict=%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s, http.MethodPost, "/v1/beds/one/checkpoint", nil, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"persistence":"noop"`) {
		t.Fatalf("checkpoint=%d %s", rec.Code, rec.Body.String())
	}
	for _, payload := range []string{`{"id":"bad","sync":"s3","pillows":[{"name":"main","mounts":[{"volume":"workspace","path":"/workspace"}]}]}`, `{"id":"bad","sync":"invalid","pillows":[{"name":"main","mounts":[{"volume":"workspace","path":"/workspace"}]}]}`, `{"id":"bad","sync":12,"pillows":[{"name":"main","mounts":[{"volume":"workspace","path":"/workspace"}]}]}`, `{"id":"bad","store":"noop","pillows":[{"name":"main","mounts":[{"volume":"workspace","path":"/workspace"}]}]}`} {
		rec = do(t, s, http.MethodPost, "/v1/beds", strings.NewReader(payload), headers)
		if rec.Code != 400 {
			t.Fatalf("invalid store=%d %s", rec.Code, rec.Body.String())
		}
	}
	rec = do(t, s, http.MethodGet, "/v1/beds/capabilities", nil, nil)
	if !strings.Contains(rec.Body.String(), `"volume_persistence":true`) {
		t.Fatalf("capabilities=%s", rec.Body.String())
	}
}

func TestManagedSyncRequiresDurableBackend(t *testing.T) {
	s := newTestServer(t)
	rec := do(t, s, http.MethodPost, "/v1/beds", strings.NewReader(`{"id":"durable","volumes":[{"name":"workspace","managed":{"persistence":"sync"}}],"pillows":[{"name":"main","mounts":[{"volume":"workspace","path":"/workspace"}]}]}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "durable Store") {
		t.Fatalf("create=%d %s", rec.Code, rec.Body.String())
	}
}
