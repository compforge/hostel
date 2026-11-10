//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/tool"
)

// +case:id=private_temporary_directory,desc=`Two Beds write identical temporary names through commands, Sessions and Pillows`,expect=`data remains Bed-local and private TMPDIR works across all process views`
func TestBedTemporaryDirectory(t *testing.T) {
	requireTestBinary(t)
	backends := []string{"local"}
	if runtime.GOOS == "linux" {
		backends = append(backends, "supervisor")
	}
	for _, mode := range []string{"carrier", "pathshim", "proot", "mount"} {
		for _, backend := range backends {
			t.Run(mode+"/"+backend, func(t *testing.T) {
				options := restrictedOptions()
				if mode != "carrier" {
					if runtime.GOOS != "linux" {
						t.Skip("process path helpers require Linux")
					}
					helper, variable := mode, ""
					switch mode {
					case "pathshim":
						variable = pathshimEnv
						options.Bed.Filesystem.Pathshim = value(tool.Required)
					case "proot":
						variable = prootEnv
						options.Bed.Filesystem.PRoot = value(tool.Required)
					case "mount":
						helper = "bwrap"
						options.Bed.Filesystem.Bwrap = value(tool.Required)
					}
					if _, err := exec.LookPath(helper); err != nil && os.Getenv(variable) == "" {
						t.Skip(helper + " unavailable")
					}
				}
				c := startTarget(t, targetOptions{executor: backend, config: &options}).client
				checkBedTemporaryDirectory(t, c, mode)
			})
		}
	}
}

func checkBedTemporaryDirectory(t *testing.T, c *apiClient, mode string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	var health struct {
		ProcessView struct {
			Mode string `json:"mode"`
		} `json:"process_view"`
	}
	response, err := c.json(ctx, "GET", "/healthz", "", nil, &health)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "temporary view", response)
	if health.ProcessView.Mode != mode {
		t.Fatalf("view=%s want=%s", health.ProcessView.Mode, mode)
	}
	// A Pillow and commands must use the same directory despite separate launches.
	const worker = `set -eu
while :; do
 if test -f "$TMPDIR/session-input"; then
  printf '%s:' "$MARKER" > "$TMPDIR/pillow-output.next"
  cat "$TMPDIR/session-input" >> "$TMPDIR/pillow-output.next"
  mv "$TMPDIR/pillow-output.next" "$TMPDIR/pillow-output"
 fi
 sleep 0.1
done`
	beds := []string{"temporary-a", "temporary-b"}
	for _, id := range beds {
		workdir := "/workspace"
		if id == "temporary-b" {
			workdir = "/tmp"
		}
		response, err = c.json(ctx, "POST", "/v1/beds", "", map[string]any{"id": id, "pillows": []map[string]any{{"name": "temporary-worker", "workdir": workdir, "command": []string{"/bin/sh", "-c", worker}, "env": map[string]string{"MARKER": id}, "required": true, "restart_policy": "never", "stop_seconds": 1}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "create temporary Bed", response)
		c.waitBed(t, id, func(b bedView) bool { return b.Status.Readiness.Ready }, "temporary worker")
	}
	for _, id := range beds {
		command := `mkdir -m 700 "$TMPDIR/capture" && (printf '%s' "$BED_ID"; printf error >&2) > "$TMPDIR/capture/stdout" 2> "$TMPDIR/capture/stderr"`
		if mode != "carrier" {
			command = `test "$TMPDIR" = /tmp && ` + strings.ReplaceAll(command, "$TMPDIR", "/tmp")
		}
		result, response := c.command(t, id, map[string]any{"command": command, "timeout": 5000})
		must2xx(t, "capture command", response)
		assertCommandExit(t, result, 0)
		must2xx(t, "API creates session input", c.upload(t, id, "/tmp/from-api", []byte(id)))
		var session struct {
			ID string `json:"session_id"`
		}
		response, err := c.json(ctx, "POST", "/session", id, map[string]string{"cwd": "/workspace"}, &session)
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "create temporary session", response)
		result, response = c.stream(t, "/session/"+url.PathEscape(session.ID)+"/run", id, map[string]any{"command": `cat "$TMPDIR/from-api" > "$TMPDIR/session-input"`, "timeout": 5000})
		must2xx(t, "session reads API file", response)
		assertCommandExit(t, result, 0)
		response, err = c.json(ctx, "DELETE", "/session/"+url.PathEscape(session.ID), id, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "close temporary session", response)
	}
	// Check after both Beds wrote the same names, detecting last-writer-wins leaks.
	for _, id := range beds {
		waitRootFile(t, c, id, "/tmp/pillow-output", id+":"+id)
		for _, file := range []struct{ name, want string }{{"stdout", id}, {"stderr", "error"}} {
			response, err := c.request(ctx, "GET", "/v1/beds/"+id+"/execd/files/download?path=/tmp/capture/"+file.name, "", nil, "")
			if err != nil || response.Status != 200 || string(response.Body) != file.want {
				t.Fatalf("%s download %s: %+v %v", id, file.name, response, err)
			}
		}
		response, err := c.request(ctx, "DELETE", "/v1/beds/"+id+"/execd/directories?path=/tmp/capture", "", nil, "")
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "remove capture", response)
		result, response := c.command(t, id, map[string]any{"command": `test ! -e "$TMPDIR/capture" && test "$(cat "$TMPDIR/pillow-output")" = ` + fmt.Sprintf("'%s:%s'", id, id), "timeout": 5000})
		must2xx(t, "process observes API deletion", response)
		assertCommandExit(t, result, 0)
	}
	for _, id := range beds {
		response, err := c.json(ctx, "DELETE", "/v1/beds/"+id+"?purge=true", "", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "purge temporary Bed", response)
	}
}

// +case:id=explicit_temporary_volume,desc=`Replace default private scratch with managed or shared HostPath data`,expect=`commands and APIs agree on ownership and Bed purge preserves provider-owned data`
func TestBedTemporaryVolume(t *testing.T) {
	requireTestBinary(t)
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux path helpers")
	}
	for _, variant := range []struct{ mode, backend, source string }{
		{"pathshim", "local", "managed"}, {"pathshim", "supervisor", "managed"},
		{"mount", "local", "managed"}, {"mount", "supervisor", "managed"},
		{"pathshim", "local", "host_path"}, {"pathshim", "supervisor", "host_path"},
		{"mount", "local", "host_path"}, {"mount", "supervisor", "host_path"},
	} {
		t.Run(variant.mode+"/"+variant.backend+"/"+variant.source, func(t *testing.T) {
			helper := "pathshim"
			if variant.mode == "mount" {
				helper = "bwrap"
			}
			if _, err := exec.LookPath(helper); err != nil {
				t.Skip(helper + " unavailable")
			}
			options := restrictedOptions()
			if variant.mode == "mount" {
				options.Bed.Filesystem.Bwrap = value(tool.Required)
			} else {
				options.Bed.Filesystem.Pathshim = value(tool.Required)
			}
			c := startTarget(t, targetOptions{executor: variant.backend, config: &options}).client
			// Path helpers enter sources as the Bed UID, so ancestors must be
			// traversable too; testing.T's private parent is not a public grant.
			shared, err := os.MkdirTemp("/tmp", "hostel-shared-scratch-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(shared)
			if err := os.Chmod(shared, 0777); err != nil {
				t.Fatal(err)
			}
			sentinel, err := os.CreateTemp("/tmp", "hostel-public-sentinel-")
			if err != nil {
				t.Fatal(err)
			}
			sentinel.Close()
			defer os.Remove(sentinel.Name())
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			for _, id := range []string{"scratch-a", "scratch-b"} {
				volume := map[string]any{"name": "scratch", "managed": map[string]any{"persistence": "none"}}
				if variant.source == "host_path" {
					volume = map[string]any{"name": "scratch", "host_path": map[string]any{"path": shared}}
				}
				response, err := c.json(ctx, "POST", "/v1/beds", "", map[string]any{"id": id, "volumes": []map[string]any{volume}, "pillows": []map[string]any{{"name": "main", "workdir": "/tmp", "mounts": []map[string]any{{"volume": "scratch", "path": "/tmp"}}}}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				must2xx(t, "create mapped scratch", response)
				c.waitBed(t, id, func(b bedView) bool { return b.Status.Readiness.Ready }, "mapped scratch ready")
				result, response := c.command(t, id, map[string]any{"command": `test "$TMPDIR" = /tmp && test ! -e /tmp/` + filepath.Base(sentinel.Name()) + ` && printf '%s' "$BED_ID" > "/tmp/$BED_ID"`, "timeout": 5000})
				must2xx(t, "write mapped scratch", response)
				assertCommandExit(t, result, 0)
			}
			for _, id := range []string{"scratch-a", "scratch-b"} {
				waitRootFile(t, c, id, "/tmp/"+id, id)
				other := "scratch-a"
				if id == other {
					other = "scratch-b"
				}
				command := "test ! -e /tmp/" + other
				if variant.source == "host_path" {
					waitRootFile(t, c, id, "/tmp/"+other, other)
					command = `test "$(cat /tmp/` + other + `)" = ` + other
				}
				result, response := c.command(t, id, map[string]any{"command": command, "timeout": 5000})
				must2xx(t, "check temporary ownership", response)
				assertCommandExit(t, result, 0)
			}
			for _, id := range []string{"scratch-a", "scratch-b"} {
				response, err := c.json(ctx, "DELETE", "/v1/beds/"+id+"?purge=true", "", nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				must2xx(t, "purge mapped scratch", response)
				if variant.source == "host_path" {
					if data, err := os.ReadFile(filepath.Join(shared, id)); err != nil || string(data) != id {
						t.Fatalf("purge changed provider scratch: %q %v", data, err)
					}
				}
			}
			if _, err := os.Stat(sentinel.Name()); err != nil {
				t.Fatalf("purge removed Carrier scratch: %v", err)
			}
		})
	}
}

// +case:id=temporary_carrier_root,desc=`Put BedHome below carrier /tmp and switch a /tmp-workdir Pillow session to /workspace`,expect=`private carrier paths survive the guest /tmp mapping`
func TestPathshimBedHomeUnderTemporaryDirectory(t *testing.T) {
	requireTestBinary(t)
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux pathshim")
	}
	if _, err := exec.LookPath("pathshim"); err != nil && os.Getenv(pathshimEnv) == "" {
		t.Skip("pathshim unavailable")
	}
	for _, backend := range []string{"local", "supervisor"} {
		t.Run(backend, func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "hostel-e2e-overlap-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			if err := os.Chmod(root, 0755); err != nil {
				t.Fatal(err)
			}
			options := restrictedOptions()
			options.Bed.Filesystem.Pathshim = value(tool.Required)
			c := startTarget(t, targetOptions{executor: backend, config: &options, bedsRoot: root}).client
			checkBedTemporaryDirectory(t, c, "pathshim")
		})
	}
}
