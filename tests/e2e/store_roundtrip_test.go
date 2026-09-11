//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMixedBedStoresRoundTrip uses a real S3-compatible service and two daemon
// workspace roots: a successful restore cannot accidentally reuse local luggage.
func TestMixedBedStoresRoundTrip(t *testing.T) {
	if os.Getenv("HOSTEL_E2E_REQUIRE_S3") != "1" {
		t.Skip("set HOSTEL_E2E_REQUIRE_S3=1 with a disposable S3 endpoint")
	}
	if os.Getenv(binaryEnv) == "" || os.Getenv(imageEnv) != "" {
		t.Fatal("S3 round trip requires the binary profile")
	}
	objects := newS3Fixture(t)
	t.Setenv("HOSTEL_PERSISTED_PATHS", "/workspace")
	root := filepath.Join(t.TempDir(), "source")
	source := startTarget(t, targetOptions{store: "noop", maxBeds: 8, workspaceRoot: root}).client
	beds := []struct{ id, store string }{
		{"cas-a", "cas"}, {"cas-b", "cas"}, {"pack-a", "pack"}, {"tar-a", "tar"}, {"noop-a", "noop"},
	}
	payloads := make(map[string][]byte)
	for _, b := range beds {
		createStoredBed(t, source, b.id, b.store)
		// Same paths and different binary contents reveal cross-Bed restore mixups.
		payload := bytes.Repeat([]byte(b.id+"\x00\xff\n"), 32768)
		payloads[b.id] = payload
		must2xx(t, "upload "+b.id, source.upload(t, b.id, "/workspace/nested/data.bin", payload))
		bedSyncAction(t, source, "POST", "/v1/beds/"+b.id+"/checkpoint")
	}
	if n := objects.count(t); n == 0 {
		t.Fatal("checkpoint created no remote objects")
	}
	for _, b := range beds {
		bedSyncAction(t, source, "DELETE", "/v1/beds/"+b.id)
		if _, err := os.Stat(filepath.Join(root, b.id)); !os.IsNotExist(err) {
			t.Fatalf("%s local copy survived eviction: %v", b.id, err)
		}
	}

	// Explicit per-Bed choices must survive a different instance default as well.
	destination := startTarget(t, targetOptions{store: "tar", maxBeds: 8}).client
	for _, b := range beds {
		createStoredBed(t, destination, b.id, b.store)
		assertStoredPayload(t, destination, b.id, b.store, payloads[b.id])
	}
	for _, b := range beds {
		// Re-restore siblings after earlier purges, so local files cannot hide an
		// over-broad remote deletion (including two Beds using the same format).
		bedSyncAction(t, destination, "DELETE", "/v1/beds/"+b.id)
		createStoredBed(t, destination, b.id, b.store)
		assertStoredPayload(t, destination, b.id, b.store, payloads[b.id])
		bedSyncAction(t, destination, "DELETE", "/v1/beds/"+b.id+"?purge=true")
		createStoredBed(t, destination, b.id, b.store)
		if got := destination.download(t, b.id, "/workspace/nested/data.bin"); got.Status != http.StatusNotFound {
			t.Fatalf("%s restored purged data: status=%d", b.id, got.Status)
		}
		bedSyncAction(t, destination, "DELETE", "/v1/beds/"+b.id+"?purge=true")
	}
	if n := objects.count(t); n != 0 {
		t.Fatalf("purge left %d remote objects", n)
	}
}

func createStoredBed(t *testing.T, c *apiClient, id, kind string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := c.json(ctx, "POST", "/v1/beds", "", map[string]string{"id": id, "sync": kind}, nil)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "create "+id, response)
	c.waitBed(t, id, func(b bedView) bool { return b.Status.Readiness.Ready && b.Sync == kind }, "ready with selected Store")
}

func bedSyncAction(t *testing.T, c *apiClient, method, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	response, err := c.json(ctx, method, path, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, path, response)
}

func assertStoredPayload(t *testing.T, c *apiClient, id, kind string, want []byte) {
	t.Helper()
	got := c.download(t, id, "/workspace/nested/data.bin")
	if kind == "noop" {
		if got.Status != http.StatusNotFound {
			t.Fatalf("noop restored data: status=%d", got.Status)
		}
		return
	}
	if got.Status != http.StatusOK || !bytes.Equal(got.Body, want) {
		t.Fatalf("%s restore mismatch: status=%d bytes=%d want=%d", id, got.Status, len(got.Body), len(want))
	}
}
