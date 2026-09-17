//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"github.com/qiankunli/hostel/internal/bed/tool"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

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
				room := "dorm"
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
						room = "suite"
						options.Bed.Filesystem.Bwrap = value(tool.Required)
					}
					if _, err := exec.LookPath(helper); err != nil && os.Getenv(variable) == "" {
						t.Skip(helper + " unavailable")
					}
				}
				c := startTarget(t, targetOptions{isolation: room, executor: backend, config: &options}).client
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
	// A Service and commands must use the same directory despite separate launches.
	const worker = `set -eu
while :; do
 if test -f "$TMPDIR/session-input"; then
  printf '%s:' "$MARKER" > "$TMPDIR/service-output.next"
  cat "$TMPDIR/session-input" >> "$TMPDIR/service-output.next"
  mv "$TMPDIR/service-output.next" "$TMPDIR/service-output"
 fi
 sleep 0.1
done`
	beds := []string{"temporary-a", "temporary-b"}
	for _, id := range beds {
		response, err = c.json(ctx, "POST", "/v1/beds", "", map[string]any{"id": id, "services": []map[string]any{{"name": "temporary-worker", "command": []string{"/bin/sh", "-c", worker}, "env": map[string]string{"MARKER": id}, "required": true, "restart": "never", "stop_seconds": 1}}}, nil)
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
		waitRootFile(t, c, id, "/tmp/service-output", id+":"+id)
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
		result, response := c.command(t, id, map[string]any{"command": `test ! -e "$TMPDIR/capture" && test "$(cat "$TMPDIR/service-output")" = ` + fmt.Sprintf("'%s:%s'", id, id), "timeout": 5000})
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
