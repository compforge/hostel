//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/tool"
)

// A Service is a supervised execution in the same Bed filesystem as commands.
// Both Beds use identical absolute paths, including a previously absent root
// directory; no external mapping or pre-populated carrier directory is supplied.
func TestBedRootServiceCommandFiles(t *testing.T) {
	requireTestBinary(t)
	if runtime.GOOS != "linux" {
		t.Skip("native Bed root views require Linux bwrap or PRoot")
	}
	for _, mode := range []string{"mount", "proot"} {
		for _, backend := range []string{"local", "supervisor"} {
			t.Run(mode+"/"+backend, func(t *testing.T) {
				options := restrictedOptions()
				room := "dorm"
				if mode == "mount" {
					if _, err := exec.LookPath("bwrap"); err != nil {
						t.Skip("bwrap not installed")
					}
					options.Bed.Filesystem.Bwrap = value(tool.Required)
					room = "suite"
				} else {
					if _, err := exec.LookPath("proot"); err != nil && os.Getenv(prootEnv) == "" {
						t.Skip("PRoot not installed; set HOSTEL_E2E_PROOT to require it")
					}
					options.Bed.Filesystem.PRoot = value(tool.Required)
				}
				c := startTarget(t, targetOptions{isolation: room, executor: backend, config: &options}).client
				var health struct {
					ProcessView struct {
						Mode   string `json:"mode"`
						Rootfs bool   `json:"rootfs"`
					} `json:"process_view"`
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				response, err := c.json(ctx, "GET", "/healthz", "", nil, &health)
				cancel()
				if err != nil {
					t.Fatal(err)
				}
				must2xx(t, "root view health", response)
				if health.ProcessView.Mode != mode || !health.ProcessView.Rootfs {
					t.Fatalf("root view degraded: %+v", health)
				}
				checkBedRootServiceFiles(t, c)
			})
		}
	}
}

func checkBedRootServiceFiles(t *testing.T, c *apiClient) {
	t.Helper()
	const worker = `set -eu
mkdir -p /mnt/jobs /session-cache /tmp
while :; do
  for dir in /mnt/jobs /session-cache /tmp; do
    if [ -f "$dir/input" ]; then
      request=$(cat "$dir/input")
      printf '%s' "$MARKER:$request" > "$dir/output.next"
      mv "$dir/output.next" "$dir/output"
    fi
  done
  sleep 0.1
done`
	beds := []string{"root-a", "root-b"}
	for _, id := range beds {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			response, err := c.json(ctx, "DELETE", "/v1/beds/"+id+"?purge=true", "", nil, nil)
			if err != nil || response.Status != http.StatusOK {
				t.Errorf("cleanup %s: status=%d error=%v body=%s", id, response.Status, err, response.Body)
			}
		})
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		response, err := c.json(ctx, "POST", "/v1/beds", "", map[string]any{
			"id": id,
			"services": []map[string]any{{
				"name": "writer", "command": []string{"/bin/sh", "-c", worker},
				"env": map[string]string{"MARKER": id}, "required": true,
				"restart": "never", "stop_seconds": 1,
			}},
		}, nil)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "create Bed with worker", response)
	}
	for _, id := range beds {
		c.waitBed(t, id, func(b bedView) bool { return b.Status.Readiness.Ready }, "required Service ready")
	}
	for _, writer := range []string{"file-api", "executor"} {
		t.Run(writer, func(t *testing.T) {
			checkBedRootFileRounds(t, c, beds, writer)
		})
	}
}

func checkBedRootFileRounds(t *testing.T, c *apiClient, beds []string, writer string) {
	t.Helper()
	dirs := []string{"/mnt/jobs", "/session-cache", "/tmp"}
	for round := 0; round < 3; round++ {
		input := fmt.Sprintf("%s-round-%d", writer, round)
		// Both workers remain live while requests and assertions are interleaved.
		for _, id := range beds {
			// Different payloads also detect a Service reading the other Bed's
			// input, even when its output happens to be correctly isolated.
			if writer == "file-api" {
				for _, dir := range dirs {
					must2xx(t, "send worker input", c.upload(t, id, dir+"/input", []byte(id+":"+input)))
				}
			} else {
				result, response := c.command(t, id, map[string]any{
					"command": `set -eu
for dir in /mnt/jobs /session-cache /tmp; do
  printf '%s' "$INPUT" > "$dir/input.next"
  mv "$dir/input.next" "$dir/input"
done`,
					"envs": map[string]string{"INPUT": id + ":" + input}, "cwd": "/", "timeout": 5000,
				})
				must2xx(t, "Executor writes Service input", response)
				assertCommandExit(t, result, 0)
			}
		}
		for _, id := range beds {
			for _, dir := range dirs {
				waitRootFile(t, c, id, dir+"/output", id+":"+id+":"+input)
			}
		}
		// Recheck after BOTH writers finished, so a last-writer-wins shared file
		// cannot pass by being read immediately after its own write.
		for _, id := range beds {
			for _, dir := range dirs {
				for _, fileName := range []string{"input", "output"} {
					path, want := dir+"/"+fileName, id+":"+input
					if fileName == "output" {
						want = id + ":" + want
					}
					result, response := c.command(t, id, map[string]any{"command": "cat " + path, "cwd": "/", "timeout": 5000})
					must2xx(t, "Executor reads shared Bed file", response)
					assertCommandExit(t, result, 0)
					if result.Stdout != want {
						t.Fatalf("bed=%s path=%s command=%q want=%q", id, path, result.Stdout, want)
					}
					file := c.download(t, id, path)
					if file.Status != http.StatusOK || string(file.Body) != result.Stdout {
						t.Fatalf("bed=%s path=%s API=%q command=%q", id, path, file.Body, result.Stdout)
					}
				}
			}
		}
	}
}

func waitRootFile(t *testing.T, c *apiClient, bed, path, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var last httpResult
	for ctx.Err() == nil {
		var err error
		last, err = c.request(ctx, "GET", "/files/download?path="+url.QueryEscape(path), bed, nil, "")
		if err == nil && last.Status == http.StatusOK && string(last.Body) == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("worker output bed=%s path=%s status=%d body=%q want=%q", bed, path, last.Status, last.Body, want)
}
