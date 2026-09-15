package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	apiview "github.com/qiankunli/hostel/internal/api/view"
)

func TestBedRenewExpirationHTTP(t *testing.T) {
	s := newTestServer(t)
	s.mgr.SetBedIdleTTL(time.Minute)
	createBedAndWait(t, s, "renew")
	until := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	body := `{"expiresAt":"` + until.Format(time.RFC3339) + `"}`
	rec := do(t, s, http.MethodPost, "/v1/beds/renew/renew-expiration", strings.NewReader(body), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var response apiview.BedExpiration
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ExpiresAt == nil || !response.ExpiresAt.Equal(until) {
		t.Fatalf("response=%s", rec.Body.String())
	}
	b, _ := s.mgr.Get("renew")
	if !b.RetainUntil().Equal(until) || b.Inflight() != 0 || b.Status().Pinned {
		t.Fatal("HTTP renewal changed more than retention")
	}
	rec = do(t, s, http.MethodDelete, "/v1/beds/renew", nil, nil)
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s, http.MethodPost, "/v1/beds/renew/renew-expiration", strings.NewReader(body), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing: %d %s", rec.Code, rec.Body.String())
	}
	if _, exists := s.mgr.Get("renew"); exists {
		t.Fatal("renew recreated bed")
	}
}

func TestBedRenewExpirationBadRequests(t *testing.T) {
	s := newTestServer(t)
	createBedAndWait(t, s, "renew")
	for _, body := range []string{"{", `{"expiresAt":123}`, `{"expiresAt":"not-a-date"}`, `{"expiresAt":"0001-01-01T00:00:00Z"}`, `{"expiresAt":"2000-01-01T00:00:00Z"}`} {
		rec := do(t, s, http.MethodPost, "/v1/beds/renew/renew-expiration", strings.NewReader(body), nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body=%q: %d %s", body, rec.Code, rec.Body.String())
		}
	}
}

func TestBedRenewExpirationUnlimitedHTTP(t *testing.T) {
	s := newTestServer(t)
	createBedAndWait(t, s, "renew")
	body := `{"expiresAt":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`
	rec := do(t, s, http.MethodPost, "/v1/beds/renew/renew-expiration", strings.NewReader(body), nil)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"expiresAt":null}` {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestBedRenewExpirationWithoutTimestampHTTP(t *testing.T) {
	s := newTestServer(t)
	s.mgr.SetBedIdleTTL(time.Minute)
	createBedAndWait(t, s, "renew")
	for _, body := range []string{"", "{}", `{"expiresAt":null}`} {
		before := time.Now()
		rec := do(t, s, http.MethodPost, "/v1/beds/renew/renew-expiration", strings.NewReader(body), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("body=%q: %d %s", body, rec.Code, rec.Body.String())
		}
		var response apiview.BedExpiration
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.ExpiresAt == nil || response.ExpiresAt.Before(before.Add(time.Minute)) ||
			response.ExpiresAt.After(time.Now().Add(time.Minute)) {
			t.Fatalf("response=%s", rec.Body.String())
		}
	}
}
