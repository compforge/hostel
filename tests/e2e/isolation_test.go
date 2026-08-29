//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestIsolationLevels(t *testing.T) {
	required := requiredIsolationLevels()
	for _, requested := range []string{"dorm", "room", "suite"} {
		t.Run(requested, func(t *testing.T) {
			target := startTarget(t, targetOptions{isolation: requested, maxBeds: 3})
			c := target.client
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var health healthView
			result, err := c.json(ctx, "GET", "/healthz", "", nil, &health)
			if err != nil || result.Status != http.StatusOK {
				t.Fatalf("healthz: status=%d err=%v body=%s", result.Status, err, result.Body)
			}
			if health.Isolation.Requested != requested || health.Isolation.Level != health.Isolation.Effective {
				t.Fatalf("isolation resolution: %+v", health.Isolation)
			}
			if required[requested] && health.Isolation.Effective != requested {
				t.Fatalf("required isolation %s degraded to %s (ceiling=%s mechanism=%s)", requested, health.Isolation.Effective, health.Isolation.Ceiling, health.Isolation.Mechanism)
			}
			if health.WorkspaceMount != (health.Isolation.Effective == "suite") {
				t.Fatalf("workspace_mount=%v for effective isolation %s", health.WorkspaceMount, health.Isolation.Effective)
			}
			t.Logf("requested=%s effective=%s ceiling=%s mechanism=%s", requested, health.Isolation.Effective, health.Isolation.Ceiling, health.Isolation.Mechanism)

			write, response := c.command(t, "isolation-a", map[string]any{
				"command": "printf secret-a > secret.txt; pwd",
				"timeout": 30_000,
			})
			must2xx(t, "write isolation probe", response)
			assertCommandExit(t, write, 0)
			bedA := c.waitBed(t, "isolation-a", func(b bedView) bool {
				return b.Status.Phase == "resident" && b.Status.Readiness.Ready && b.Workspace != ""
			}, "ready with workspace")

			read, response := c.command(t, "isolation-b", map[string]any{
				"command": "cat \"$TARGET\"",
				"envs":    map[string]string{"TARGET": bedA.Workspace + "/secret.txt"},
				"timeout": 30_000,
			})
			must2xx(t, "cross-bed isolation probe", response)
			switch health.Isolation.Effective {
			case "dorm":
				assertCommandExit(t, read, 0)
				if read.Stdout != "secret-a" {
					t.Fatalf("dorm did not expose shared carrier path: stdout=%q stderr=%q", read.Stdout, read.Stderr)
				}
			case "room", "suite":
				if read.Result == nil || read.Result.Process.ExitCode == nil || *read.Result.Process.ExitCode == 0 {
					t.Fatalf("%s allowed sibling workspace read: %+v stdout=%q stderr=%q", health.Isolation.Effective, read.Result, read.Stdout, read.Stderr)
				}
			default:
				t.Fatalf("unknown effective isolation %q", health.Isolation.Effective)
			}

			pwd, response := c.command(t, "isolation-a", map[string]any{"command": "pwd", "timeout": 30_000})
			must2xx(t, "workspace path probe", response)
			assertCommandExit(t, pwd, 0)
			if health.Isolation.Effective == "suite" && strings.TrimSpace(pwd.Stdout) != "/workspace" {
				t.Fatalf("suite cwd=%q, want /workspace", strings.TrimSpace(pwd.Stdout))
			}
		})
	}
}

func requiredIsolationLevels() map[string]bool {
	required := map[string]bool{}
	for _, level := range strings.Split(os.Getenv("HOSTEL_E2E_REQUIRE_ISOLATION"), ",") {
		if level = strings.TrimSpace(level); level != "" {
			required[level] = true
		}
	}
	return required
}
