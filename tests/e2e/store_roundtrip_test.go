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

// +case:id=managed_volume_roundtrip,desc=`Two Beds checkpoint multiple managed volumes and restore on a fresh Carrier with changed mount paths`,expect=`sync data remains isolated by Bed and volume, temporary/root data is absent, purge removes the snapshots`
func TestManagedVolumeRoundTrip(t *testing.T) {
	if os.Getenv("HOSTEL_E2E_REQUIRE_S3") != "1" {
		t.Skip("set HOSTEL_E2E_REQUIRE_S3=1 with a disposable S3 endpoint")
	}
	requireTestBinary(t)
	for _, kind := range []string{"cas", "pack", "tar", "auto"} {
		t.Run(kind, func(t *testing.T) {
			objects := newS3Fixture(t)
			root := filepath.Join(t.TempDir(), "source")
			source := startTarget(t, targetOptions{store: kind, maxBeds: 8, bedsRoot: root}).client
			payloads := map[string][]byte{}
			for _, id := range []string{"bed1", "bed2"} {
				createVolumeBed(t, source, id, "/workspace", false)
				payloads[id] = bytes.Repeat([]byte(id+"\x00\xff\n"), 32768)
				must2xx(t, "write workspace", source.upload(t, id, "/workspace/result.bin", payloads[id]))
				must2xx(t, "write second volume", source.upload(t, id, "/memory/note", []byte("memory-"+id)))
				must2xx(t, "write temporary", source.upload(t, id, "/run/shared/token", []byte("temporary")))
				must2xx(t, "write rootfs", source.upload(t, id, "/tmp/marker", []byte("temporary")))
				bedSyncAction(t, source, "POST", "/v1/beds/"+id+"/checkpoint")
				bedSyncAction(t, source, "DELETE", "/v1/beds/"+id)
				if _, err := os.Stat(filepath.Join(root, id)); !os.IsNotExist(err) {
					t.Fatalf("local copy survived: %v", err)
				}
			}
			if objects.count(t) == 0 {
				t.Fatal("no durable objects")
			}
			destination := startTarget(t, targetOptions{store: "auto", maxBeds: 8}).client
			for _, id := range []string{"bed1", "bed2"} {
				if response := destination.download(t, id, "/data/result.bin"); response.Status != http.StatusNotFound {
					t.Fatalf("undeclared Bed access restored data: %d", response.Status)
				}
				createVolumeBed(t, destination, id, "/data", true)
				got := destination.download(t, id, "/data/result.bin")
				if got.Status != http.StatusOK || !bytes.Equal(got.Body, payloads[id]) {
					t.Fatalf("%s workspace restore mismatch: status=%d bytes=%d", id, got.Status, len(got.Body))
				}
				got = destination.download(t, id, "/memory/note")
				if got.Status != http.StatusOK || string(got.Body) != "memory-"+id {
					t.Fatalf("second volume: %d %q", got.Status, got.Body)
				}
				for _, path := range []string{"/run/shared/token", "/tmp/marker", "/workspace/result.bin"} {
					if got := destination.download(t, id, path); got.Status != http.StatusNotFound {
						t.Fatalf("unexpected restored path %s: %d", path, got.Status)
					}
				}
				bedSyncAction(t, destination, "DELETE", "/v1/beds/"+id+"?purge=true")
			}
			if objects.count(t) != 0 {
				t.Fatal("purge left remote objects")
			}
		})
	}
}

func volumeRequest(id, policy string) map[string]any {
	return map[string]any{"id": id, "volumes": []map[string]any{{"name": "workspace", "managed": map[string]string{"persistence": policy}}}, "pillows": workspacePillows()}
}

func createVolumeBed(t *testing.T, c *apiClient, id, mount string, restore bool) {
	t.Helper()
	request := volumeRequest(id, "sync")
	request["volumes"].([]map[string]any)[0]["name"] = "data"
	request["volumes"] = append(request["volumes"].([]map[string]any),
		map[string]any{"name": "memory", "managed": map[string]string{"persistence": "sync"}},
		map[string]any{"name": "runtime", "managed": map[string]string{"persistence": "none"}})
	request["pillows"] = []map[string]any{{"name": "agent", "workdir": mount, "mounts": []map[string]any{
		{"volume": "data", "path": mount}, {"volume": "memory", "path": "/memory"}, {"volume": "runtime", "path": "/run/shared"},
	}}}
	// Restoration supplies workload configuration while Store supplies the Volume contract.
	if restore {
		delete(request, "volumes")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	response, err := c.json(ctx, "POST", "/v1/beds", "", request, nil)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "create "+id, response)
	c.waitBed(t, id, func(b bedView) bool { return b.Status.Readiness.Ready }, "managed volumes ready")
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
