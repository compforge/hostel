//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/tool"
	"github.com/qiankunli/hostel/protocol"
)

// +case:id=bed_volume_consumers,desc=`Two Beds run Pillows sharing a volume with read-only commands and file APIs`,expect=`writes propagate only within the owning Bed, mutations fail for readers, and Pillow restart preserves data`
func TestBedVolumeConsumers(t *testing.T) {
	requireTestBinary(t)
	if runtime.GOOS != "linux" {
		t.Skip("process mount permissions require Linux")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap not installed")
	}
	for _, variant := range []struct{ backend, source string }{{"local", "managed"}, {"supervisor", "managed"}, {"local", "host_path"}, {"supervisor", "host_path"}} {
		backend := variant.backend
		t.Run(backend+"/"+variant.source, func(t *testing.T) {
			options := restrictedOptions()
			options.Bed.Filesystem.Bwrap = value(tool.Required)
			c := startTarget(t, targetOptions{executor: backend, config: &options}).client
			for _, id := range []string{"volume-a", "volume-b"} {
				id := id
				volume := map[string]any{"name": "shared", "managed": map[string]any{"persistence": "none"}}
				var hostSource string
				if variant.source == "host_path" {
					hostSource = t.TempDir()
					// This fixture explicitly grants access to the dynamically allocated Bed UID.
					if err := os.Chmod(hostSource, 0777); err != nil {
						t.Fatal(err)
					}
					volume = map[string]any{"name": "shared", "host_path": map[string]any{"path": hostSource}}
				}
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					response, err := c.json(ctx, "DELETE", "/v1/beds/"+id+"?purge=true", "", nil, nil)
					if err != nil || response.Status != http.StatusOK {
						t.Errorf("cleanup %s: status=%d error=%v body=%s", id, response.Status, err, response.Body)
					}
					if hostSource != "" {
						data, err := os.ReadFile(filepath.Join(hostSource, "preserved"))
						if err != nil || string(data) != id {
							t.Errorf("host source lost after Bed purge: %q %v", data, err)
						}
					}
				})
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				response, err := c.json(ctx, "POST", "/v1/beds", "", map[string]any{
					"id":      id,
					"volumes": []map[string]any{volume},

					"pillows": append([]map[string]any{{"name": "workspace", "mounts": []map[string]any{{"volume": "shared", "path": "/run/shared", "read_only": true}}}}, []map[string]any{
						{"name": "writer", "required": true, "stop_seconds": 1, "env": map[string]string{"MARKER": id},
							"mounts": []map[string]any{{"volume": "shared", "path": "/producer"}},
							"command": []string{"/bin/sh", "-c", `set -eu
if [ ! -f /producer/preserved ]; then printf '%s' "$MARKER" > /producer/preserved; fi
while :; do
 if [ -f /workspace/request ]; then
  printf '%s:%s' "$MARKER" "$(cat /workspace/request)" > /producer/token.next
  chmod 444 /producer/token.next
  mv /producer/token.next /producer/token
 fi
 sleep 0.1
done`}},
						{"name": "reader", "required": true, "stop_seconds": 1,
							"mounts": []map[string]any{{"volume": "shared", "path": "/consumer", "read_only": true}},
							"command": []string{"/bin/sh", "-c", `set -eu
while :; do
 if [ -f /consumer/token ]; then
  if (printf bad > /consumer/token) 2>/dev/null; then exit 41; fi
  cat /consumer/token > /workspace/observed.next
  mv /workspace/observed.next /workspace/observed
 fi
 sleep 0.1
done`}},
					}...),
				}, nil)
				cancel()
				if err != nil {
					t.Fatal(err)
				}
				must2xx(t, "create volume Bed", response)
			}
			for _, id := range []string{"volume-a", "volume-b"} {
				c.waitBed(t, id, func(b bedView) bool { return b.Status.Readiness.Ready }, "volume pillows ready")
			}
			for _, id := range []string{"volume-a", "volume-b"} {
				must2xx(t, "publish request", c.upload(t, id, "/workspace/request", []byte("first")))
			}
			for _, id := range []string{"volume-a", "volume-b"} {
				waitRootFile(t, c, id, "/run/shared/token", id+":first")
				waitRootFile(t, c, id, "/workspace/observed", id+":first")
				result, response := c.command(t, id, map[string]any{"command": `set -eu
cat /run/shared/token
if (printf bad > /run/shared/token) 2>/dev/null; then exit 42; fi
if rm /run/shared/token 2>/dev/null; then exit 43; fi
if mv /run/shared/token /run/shared/replaced 2>/dev/null; then exit 44; fi
if chmod 600 /run/shared/token 2>/dev/null; then exit 45; fi
if mv /run/shared /run/replaced 2>/dev/null; then exit 46; fi
test ! -e /producer/token
test ! -e /consumer/token`, "timeout": 5000})
				must2xx(t, "read-only command", response)
				assertCommandExit(t, result, 0)
				if result.Stdout != id+":first" {
					t.Fatalf("cross-Bed data: %q", result.Stdout)
				}
				if response := c.upload(t, id, "/run/shared/token", []byte("bad")); response.Status < 400 {
					t.Fatalf("file API bypass: %d", response.Status)
				}
				previous := waitVolumeWriter(t, c, id, "")
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				response, err := c.json(ctx, "POST", "/v1/beds/"+id+"/pillows/writer/process/restart", "", nil, nil)
				cancel()
				if err != nil {
					t.Fatal(err)
				}
				must2xx(t, "restart writer", response)
				waitVolumeWriter(t, c, id, previous)
				must2xx(t, "publish next request", c.upload(t, id, "/workspace/request", []byte("second")))
				waitRootFile(t, c, id, "/run/shared/token", id+":second")
				waitRootFile(t, c, id, "/workspace/observed", id+":second")
				waitRootFile(t, c, id, "/run/shared/preserved", id)
			}
		})
	}
}

// Restart is asynchronous: required Pillows withdraw Bed admission until the
// new execution is ready. Waiting only for Bed readiness could observe the old run.
func waitVolumeWriter(t *testing.T, c *apiClient, id, previous string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var detail struct {
		Status struct {
			Lifecycle bedLifecycleView        `json:"lifecycle"`
			Pillows   []protocol.PillowStatus `json:"pillows"`
		} `json:"status"`
	}
	for {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		response, err := c.json(ctx, "GET", "/v1/beds/"+id, "", nil, &detail)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "read writer status", response)
		for _, pillow := range detail.Status.Pillows {
			if pillow.Name == "writer" && pillow.Ready && pillow.ExecutionID != "" && pillow.ExecutionID != previous && detail.Status.Lifecycle.Readiness.Ready {
				return pillow.ExecutionID
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("writer restart timeout: %+v", detail)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// +case:id=hostpath_preserves_authority,desc=`Mount a caller-owned root-only directory into a non-root Bed`,expect=`file API and command cannot read or write it; creation and purge preserve source permissions and data`
func TestHostPathRequiresSourcePermission(t *testing.T) {
	requireTestBinary(t)
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("requires root Linux fixture and per-Bed UID")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap unavailable")
	}
	for _, backend := range []string{"local", "supervisor"} {
		t.Run(backend, func(t *testing.T) {
			source := t.TempDir()
			if err := os.WriteFile(filepath.Join(source, "seed"), []byte("external"), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(source)
			if err != nil {
				t.Fatal(err)
			}
			options := restrictedOptions()
			options.Bed.Filesystem.Bwrap = value(tool.Required)
			c := startTarget(t, targetOptions{executor: backend, config: &options}).client
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			response, err := c.json(ctx, "POST", "/v1/beds", "", map[string]any{"id": "denied-source", "volumes": []map[string]any{{"name": "external", "host_path": map[string]any{"path": source}}}, "pillows": []map[string]any{{"name": "main", "mounts": []map[string]any{{"volume": "external", "path": "/external"}}}}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "create restricted source", response)
			c.waitBed(t, "denied-source", func(b bedView) bool { return b.Status.Readiness.Ready }, "restricted source ready")
			result, response := c.command(t, "denied-source", map[string]any{"command": `test "$(id -u)" != 0 && ! cat /external/seed && ! touch /external/new`, "timeout": 5000})
			must2xx(t, "source DAC", response)
			assertCommandExit(t, result, 0)
			response, err = c.request(ctx, "GET", "/v1/beds/denied-source/execd/files/download?path=/external/seed", "", nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if response.Status < 400 {
				t.Fatalf("API bypassed source permissions: %d", response.Status)
			}
			if response := c.upload(t, "denied-source", "/external/new", []byte("bad")); response.Status < 400 {
				t.Fatalf("API wrote restricted source: %d", response.Status)
			}
			response, err = c.json(ctx, "DELETE", "/v1/beds/denied-source?purge=true", "", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "purge restricted source", response)
			after, err := os.Stat(source)
			if err != nil {
				t.Fatal(err)
			}
			a, b := before.Sys().(*syscall.Stat_t), after.Sys().(*syscall.Stat_t)
			if before.Mode() != after.Mode() || a.Uid != b.Uid || a.Gid != b.Gid {
				t.Fatal("changed external source authority")
			}
			if data, err := os.ReadFile(filepath.Join(source, "seed")); err != nil || string(data) != "external" {
				t.Fatalf("source data changed: %q %v", data, err)
			}
		})
	}
}
