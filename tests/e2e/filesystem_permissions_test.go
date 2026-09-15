//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/filesystem"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
)

// Run the same API assertions under real carrier permissions. Feature-off is a
// separate deterministic control; it does not stand in for kernel rejection.
func TestFilesystemPermissions(t *testing.T) {
	requireTestBinary(t)
	backends := []string{"local"}
	if runtime.GOOS == "linux" {
		backends = append(backends, "supervisor")
	}
	for _, scenario := range []string{"dorm", "room", "suite", "carrier"} {
		for _, backend := range backends {
			t.Run(scenario+"/"+backend, func(t *testing.T) {
				options := restrictedOptions()
				room := scenario
				if scenario == "carrier" {
					room = "dorm"
				} else {
					// Leave every filesystem mechanism on Auto. Only unrelated
					// network/cgroup probes are disabled to isolate this contract.
					options.Bed.Filesystem = filesystem.Options{}
				}
				// External data belongs to the caller and must be traversable
				// by any selected Bed UID. No pre-existing host data is touched.
				source, err := os.MkdirTemp("", "hostel-e2e-mapping-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.RemoveAll(source); err != nil {
						t.Errorf("remove mapping fixture: %v", err)
					}
				})
				if err := os.Chmod(source, 0777); err != nil {
					t.Fatal(err)
				}
				for _, dir := range []string{"rw", "ro", "native"} {
					path := filepath.Join(source, dir)
					if err := os.Mkdir(path, 0777); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(path, 0777); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(path, "seed"), []byte("carrier-"+dir), 0666); err != nil {
						t.Fatal(err)
					}
				}
				target := startTarget(t, targetOptions{isolation: room, executor: backend, config: &options})
				c := target.client
				var status struct {
					Components struct {
						Filesystem struct {
							Effective   string
							Mechanism   string
							ProcessView isolation.ProcessViewReport `json:"process_view"`
						}
					}
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				response, err := c.json(ctx, "GET", "/v1/status", "", nil, &status)
				cancel()
				if err != nil {
					t.Fatal(err)
				}
				must2xx(t, "filesystem diagnostics", response)
				fs := status.Components.Filesystem
				view := fs.ProcessView
				if view.Rootfs != (view.Mode == "mount" || view.Mode == "proot") ||
					(view.Mode == "mount") != (fs.Effective == "private") {
					t.Fatalf("inconsistent filesystem report: %+v", fs)
				}
				if scenario == "carrier" && (view.Mode != "carrier" || view.Rootfs || view.PathMappings.ReadWrite || view.PathMappings.ReadOnly) {
					t.Fatalf("disabled filesystem mechanisms: %+v", fs)
				}
				t.Logf("files=%s mechanism=%s mode=%s rootfs=%t mapping_rw=%t mapping_ro=%t reason=%s",
					fs.Effective, fs.Mechanism, view.Mode, view.Rootfs, view.PathMappings.ReadWrite, view.PathMappings.ReadOnly, view.Reason)
				checkFilesystemPermissions(t, c, source, view)
			})
		}
	}
}

func checkFilesystemPermissions(t *testing.T, c *apiClient, source string, view isolation.ProcessViewReport) {
	t.Helper()
	// Unique Bed spellings cannot accidentally resolve to existing carrier data.
	rw, ro := "/"+filepath.Base(source)+"-rw", "/"+filepath.Base(source)+"-ro"
	native := filepath.ToSlash(filepath.Join(source, "native"))
	beds := []string{"files-a", "files-b"}
	for _, bed := range beds {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		response, err := c.json(ctx, "POST", "/v1/beds", "", map[string]any{
			"id": bed,
			"path_mappings": []map[string]any{
				{"host_path": filepath.Join(source, "rw"), "bed_path": rw},
				{"host_path": filepath.Join(source, "ro"), "bed_path": ro, "read_only": true},
			},
		}, nil)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "create mapped Bed", response)
		c.waitBed(t, bed, func(b bedView) bool { return b.Status.Readiness.Ready }, "mapped Bed ready")
		must2xx(t, "write Bed root seed", c.upload(t, bed, native+"/seed", []byte(bed)))
	}
	run := func(bed, command, cwd string) commandResult {
		t.Helper()
		result, response := c.command(t, bed, map[string]any{"command": command, "cwd": cwd, "timeout": 5000})
		must2xx(t, "filesystem command", response)
		return result
	}
	readAPI := func(bed, path, want string) {
		t.Helper()
		result := c.download(t, bed, path)
		must2xx(t, "read Bed path", result)
		if string(result.Body) != want {
			t.Fatalf("bed=%s path=%s body=%q want=%q", bed, path, result.Body, want)
		}
	}
	for _, bed := range beds {
		// Structured cwd stays Bed-owned even when native absolute paths degrade.
		result := run(bed, "cat seed; printf "+bed+" > relative", native)
		assertCommandExit(t, result, 0)
		if result.Stdout != bed {
			t.Fatalf("structured cwd read %q, want %q", result.Stdout, bed)
		}
		readAPI(bed, native+"/relative", bed)
		result = run(bed, "cat "+native+"/seed", "/workspace")
		if view.Rootfs {
			assertCommandExit(t, result, 0)
			if result.Stdout != bed {
				t.Fatalf("native root read %q, want %q", result.Stdout, bed)
			}
			result = run(bed, "printf "+bed+" > "+native+"/absolute", "/workspace")
			assertCommandExit(t, result, 0)
			readAPI(bed, native+"/absolute", bed)
		} else {
			// A confined carrier view may deny the host path. A successful
			// read must expose the carrier fixture, never pretend it is Bed data.
			if result.Result == nil || result.Result.Process.ExitCode == nil {
				t.Fatalf("native root probe lacks exit status: %+v", result)
			}
			if *result.Result.Process.ExitCode == 0 && result.Stdout != "carrier-native" {
				t.Fatalf("unreported native root redirection: %+v", result)
			}
			t.Logf("bed=%s native_root_read_exit=%d stdout=%q", bed, *result.Result.Process.ExitCode, result.Stdout)
		}
		readAPI(bed, rw+"/seed", "carrier-rw")
		readAPI(bed, ro+"/seed", "carrier-ro")
		must2xx(t, "API writes mapped source", c.upload(t, bed, rw+"/"+bed, []byte(bed)))
		denied := c.upload(t, bed, ro+"/api-write", []byte("must-not-write"))
		if denied.Status < http.StatusBadRequest || !strings.Contains(string(denied.Body), "write read-only mapping") {
			t.Fatalf("read-only API write: status=%d body=%s", denied.Status, denied.Body)
		}
		for _, mapping := range []struct {
			path      string
			supported bool
			seed      string
		}{{rw, view.PathMappings.ReadWrite, "carrier-rw"}, {ro, view.PathMappings.ReadOnly, "carrier-ro"}} {
			result := run(bed, "cat "+mapping.path+"/seed", "/workspace")
			if mapping.supported {
				assertCommandExit(t, result, 0)
				if result.Stdout != mapping.seed {
					t.Fatalf("process mapping read %q, want %q", result.Stdout, mapping.seed)
				}
			} else if result.Result == nil || result.Result.Process.ExitCode == nil || *result.Result.Process.ExitCode == 0 {
				t.Fatalf("unrealized mapping unexpectedly readable: %+v", result)
			}
		}
		if view.PathMappings.ReadWrite {
			// A persistent shell must share the same external data as commands
			// and the API; a helper selected at startup alone is not evidence.
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			var session struct {
				SessionID string `json:"session_id"`
			}
			response, err := c.json(ctx, "POST", "/session", bed, map[string]string{"cwd": rw}, &session)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "mapped session", response)
			if session.SessionID == "" {
				t.Fatal("missing session id")
			}
			command := "cat seed; printf " + bed + " > " + rw + "/session-" + bed
			result, response := c.stream(t, "/session/"+url.PathEscape(session.SessionID)+"/run", bed, map[string]any{"command": command, "timeout": 5000})
			must2xx(t, "session mapped write", response)
			assertCommandExit(t, result, 0)
			if result.Stdout != "carrier-rw" {
				t.Fatalf("mapped session read: %q", result.Stdout)
			}
			readAPI(bed, rw+"/session-"+bed, bed)
		}
		if view.PathMappings.ReadOnly {
			// The source is writable by DAC; rejection must come from the
			// declared read-only process mapping, not a chmod on the fixture.
			result := run(bed, "printf forbidden > "+ro+"/process-write", "/workspace")
			if result.Result == nil || result.Result.Process.ExitCode == nil || *result.Result.Process.ExitCode == 0 {
				t.Fatalf("read-only process mapping allowed write: %+v", result)
			}
		} else if view.Rootfs {
			// An unrealized mapping has a Bed-local candidate. API reads use
			// it when present and fall back to the external source otherwise.
			result := run(bed, "printf local-"+bed+" > "+ro+"/seed", "/workspace")
			assertCommandExit(t, result, 0)
			readAPI(bed, ro+"/seed", "local-"+bed)
		}
	}
	// Read after both Beds write: root data stays separate, explicit mappings
	// share data. Purging a Bed must never delete caller-owned mapping sources.
	for _, bed := range beds {
		readAPI(bed, native+"/seed", bed)
		for _, other := range beds {
			readAPI(bed, rw+"/"+other, other)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		response, err := c.json(ctx, "DELETE", "/v1/beds/"+bed+"?purge=true", "", nil, nil)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "purge mapped Bed", response)
	}
	for _, relative := range []string{"ro/seed", "rw/files-a", "rw/files-b", "native/seed"} {
		data, err := os.ReadFile(filepath.Join(source, relative))
		want := map[string]string{"ro/seed": "carrier-ro", "rw/files-a": "files-a", "rw/files-b": "files-b", "native/seed": "carrier-native"}[relative]
		if err != nil || string(data) != want {
			t.Fatalf("source after purge %s: %q %v", relative, data, err)
		}
	}
	for _, name := range []string{"api-write", "process-write"} {
		if _, err := os.Stat(filepath.Join(source, "ro", name)); !os.IsNotExist(err) {
			t.Fatalf("read-only source mutation %s: %v", name, err)
		}
	}
	t.Logf("root API ownership, native view, mapped API/process access and source retention verified (%s)", strings.Join(beds, ","))
}
