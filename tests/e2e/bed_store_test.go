//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestBedStoreAPIOverridesInstanceDefault(t *testing.T) {
	if os.Getenv(binaryEnv) == "" {
		t.Skip("binary profile required for runner-local S3 endpoint")
	}
	var storageRequests atomic.Int64
	// A real S3 client can reach this endpoint. Noop must never contact it;
	// a default-backed control Bed must contact it and surface the denial.
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		storageRequests.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer endpoint.Close()
	t.Setenv("HOSTEL_S3_ENDPOINT", endpoint.URL)
	t.Setenv("HOSTEL_S3_BUCKET", "e2e-beds")
	t.Setenv("HOSTEL_S3_REGION", "us-east-1")
	t.Setenv("HOSTEL_S3_PATH_STYLE", "true")
	t.Setenv("HOSTEL_S3_ACCESS_KEY_ID", "e2e-key")
	t.Setenv("HOSTEL_S3_SECRET_ACCESS_KEY", "e2e-secret")
	c := startTarget(t, targetOptions{store: "cas"}).client
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	created, err := c.json(ctx, "POST", "/v1/beds", "", map[string]string{"id": "noop-bed", "store": "noop"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "create noop", created)
	c.waitBed(t, "noop-bed", func(b bedView) bool { return b.Status.Readiness.Ready }, "noop ready")
	must2xx(t, "write noop", c.upload(t, "noop-bed", "/workspace/data.txt", []byte("local data")))
	for _, action := range []struct{ method, path string }{{"POST", "/v1/beds/noop-bed/checkpoint"}, {"DELETE", "/v1/beds/noop-bed"}} {
		response, err := c.json(ctx, action.method, action.path, "", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, action.path, response)
	}
	created, err = c.json(ctx, "POST", "/v1/beds", "", map[string]string{"id": "noop-bed", "store": "noop"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "resume noop", created)
	c.waitBed(t, "noop-bed", func(b bedView) bool { return b.Status.Readiness.Ready && b.Store == "noop" }, "noop override reused")
	if got := c.download(t, "noop-bed", "/workspace/data.txt"); got.Status != 404 {
		t.Fatalf("noop restored data: %d", got.Status)
	}
	evicted, err := c.json(ctx, "DELETE", "/v1/beds/noop-bed", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "evict before purge", evicted)
	response, err := c.json(ctx, "DELETE", "/v1/beds/noop-bed?purge=true&store=noop", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "purge noop", response)
	if got := storageRequests.Load(); got != 0 {
		t.Fatalf("noop made %d S3 requests", got)
	}
	created, err = c.json(ctx, "POST", "/v1/beds", "", map[string]string{"id": "default-bed"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "create default", created)
	c.waitBed(t, "default-bed", func(b bedView) bool { return b.Status.Phase == "failed" && b.Store == "cas" }, "default Store denial")
	if storageRequests.Load() == 0 {
		t.Fatal("default Bed did not use configured S3")
	}
	// Reverse direction: an API-selected S3 Bed also overrides a noop default.
	reverse := startTarget(t, targetOptions{store: "noop"}).client
	before := storageRequests.Load()
	explicit, err := reverse.json(ctx, "POST", "/v1/beds", "", map[string]string{"id": "explicit-s3", "store": "cas"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "override noop with S3", explicit)
	reverse.waitBed(t, "explicit-s3", func(b bedView) bool { return b.Status.Phase == "failed" && b.Store == "cas" }, "explicit S3 denial")
	if storageRequests.Load() <= before {
		t.Fatal("API S3 selection did not override noop default")
	}

}
