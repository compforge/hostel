//go:build e2e

package e2e_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/tool"
)

func TestPillowExecutableUsesBedView(t *testing.T) {
	requireTestBinary(t)
	if runtime.GOOS != "linux" || os.Getenv(imageEnv) != "" {
		t.Skip("requires Linux binary fixture")
	}
	source, err := os.MkdirTemp("", "hostel-e2e-pillow-executable-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(source); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chmod(source, 0755); err != nil {
		t.Fatal(err)
	}
	const script = "#!/bin/sh\nprintf '%s' \"$MARKER\" > /workspace/\"$NAME\"\nexec /bin/sleep 300\n"
	if err := os.WriteFile(filepath.Join(source, "worker"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"mount", "proot"} {
		for _, backend := range []string{"local", "supervisor"} {
			t.Run(mode+"/"+backend, func(t *testing.T) {
				options := restrictedOptions()
				room := "dorm"
				if mode == "mount" {
					if _, err := exec.LookPath("bwrap"); err != nil {
						t.Skip("bwrap unavailable")
					}
					options.Bed.Filesystem.Bwrap, room = value(tool.Required), "suite"
				} else {
					if _, err := exec.LookPath("proot"); err != nil && os.Getenv(prootEnv) == "" {
						t.Skip("PRoot unavailable")
					}
					options.Bed.Filesystem.PRoot = value(tool.Required)
				}
				c := startTarget(t, targetOptions{isolation: room, executor: backend, config: &options}).client
				var pillows []map[string]any
				for name, command := range map[string]string{"absolute": "/tools/worker", "relative": "./worker", "path": "worker"} {
					pillows = append(pillows, map[string]any{"name": name, "command": []string{command}, "workdir": "/tools", "required": true,
						"mounts": []map[string]any{{"volume": "tools", "path": "/tools"}},
						"env":    map[string]string{"PATH": "/tools:/usr/bin:/bin", "MARKER": "bed-env", "NAME": name}, "restart_policy": "never", "stop_seconds": 1})
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				response, err := c.json(ctx, "POST", "/v1/beds", "", map[string]any{"id": "pillow-executable", "volumes": []map[string]any{{"name": "tools", "host_path": map[string]any{"path": source}}}, "pillows": pillows}, nil)
				cancel()
				if err != nil {
					t.Fatal(err)
				}
				must2xx(t, "create Bed-local Pillows", response)
				c.waitBed(t, "pillow-executable", func(b bedView) bool { return b.Status.Readiness.Ready }, "Bed-local Pillows ready")
				result, response := c.command(t, "pillow-executable", map[string]any{"command": "set -eu; for n in absolute relative path; do for i in 1 2 3 4 5; do test -s /workspace/$n && break; sleep 0.1; done; test \"$(cat /workspace/$n)\" = bed-env; done", "timeout": 5000})
				must2xx(t, "read Pillow files", response)
				assertCommandExit(t, result, 0)
			})
		}
	}
}
