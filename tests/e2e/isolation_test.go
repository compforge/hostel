//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"net/url"
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
			if strings.TrimSpace(os.Getenv(pathshimEnv)) != "" && health.Isolation.Effective != "suite" &&
				(health.WorkspaceView.Mode != "pathshim" || !health.WorkspaceView.Available) {
				t.Fatalf("required pathshim workspace view unavailable: %+v", health.WorkspaceView)
			}
			if health.WorkspaceView.Mode == "pathshim" && !health.WorkspaceView.Available {
				t.Fatalf("pathshim view reported unavailable: %+v", health.WorkspaceView)
			}

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
			if health.WorkspaceView.Mode == "mount" || health.WorkspaceView.Mode == "pathshim" {
				if strings.TrimSpace(pwd.Stdout) != "/workspace" {
					t.Fatalf("%s workspace cwd=%q, want /workspace", health.WorkspaceView.Mode, strings.TrimSpace(pwd.Stdout))
				}
				assertCanonicalWorkspaceView(t, c, health.WorkspaceView.Mode)
			}
		})
	}
}

func TestPathshimProbeFailureKeepsCommandAPIAvailable(t *testing.T) {
	target := startTarget(t, targetOptions{
		isolation: "dorm",
		pathshim:  "/definitely/missing/pathshim",
	})
	c := target.client
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var health healthView
	result, err := c.json(ctx, "GET", "/healthz", "", nil, &health)
	if err != nil || result.Status != http.StatusOK {
		t.Fatalf("healthz: status=%d err=%v body=%s", result.Status, err, result.Body)
	}
	if health.WorkspaceView.Mode != "carrier" || health.WorkspaceView.Available || health.WorkspaceView.Reason == "" {
		t.Fatalf("fallback workspace view = %+v", health.WorkspaceView)
	}
	run, response := c.command(t, "fallback-bed", map[string]any{"command": "printf available", "timeout": 30_000})
	must2xx(t, "fallback command", response)
	assertCommandExit(t, run, 0)
	if run.Stdout != "available" {
		t.Fatalf("fallback command stdout=%q stderr=%q", run.Stdout, run.Stderr)
	}
}

func assertCanonicalWorkspaceView(t *testing.T, c *apiClient, mode string) {
	t.Helper()
	const bedID = "workspace-view"
	run, response := c.command(t, bedID, map[string]any{
		"command": "printf mapped > from-cwd.txt; test \"$HOME\" = /workspace; pwd",
		"cwd":     "/workspace/subdir",
		"timeout": 30_000,
	})
	must2xx(t, mode+" explicit cwd", response)
	assertCommandExit(t, run, 0)
	if strings.TrimSpace(run.Stdout) != "/workspace/subdir" {
		t.Fatalf("%s explicit cwd=%q, want /workspace/subdir", mode, strings.TrimSpace(run.Stdout))
	}
	download := c.download(t, bedID, "/workspace/subdir/from-cwd.txt")
	if download.Status != http.StatusOK || string(download.Body) != "mapped" {
		t.Fatalf("%s command/file API mismatch: status=%d body=%q", mode, download.Status, download.Body)
	}

	executable, response := c.command(t, bedID, map[string]any{
		"command": "cp /bin/echo /workspace/workspace-echo && /workspace/workspace-echo mapped-exec",
		"timeout": 30_000,
	})
	must2xx(t, mode+" mapped executable", response)
	assertCommandExit(t, executable, 0)
	if strings.TrimSpace(executable.Stdout) != "mapped-exec" {
		t.Fatalf("%s mapped executable stdout=%q stderr=%q", mode, executable.Stdout, executable.Stderr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	var created struct {
		SessionID string `json:"session_id"`
	}
	createdResult, err := c.json(ctx, "POST", "/session", bedID, map[string]string{"cwd": "/workspace"}, &created)
	cancel()
	if err != nil || createdResult.Status != http.StatusOK || created.SessionID == "" {
		t.Fatalf("%s create workspace session: status=%d err=%v body=%s", mode, createdResult.Status, err, createdResult.Body)
	}

	sessionPath := "/session/" + url.PathEscape(created.SessionID)
	sessionRun, response := c.stream(t, sessionPath+"/run", bedID, map[string]any{
		"command": "cat > from-session.txt <<'EOF'\nsession\nEOF\npwd",
		"cwd":     "/workspace/session-subdir",
		"timeout": 30_000,
	})
	must2xx(t, mode+" session explicit cwd", response)
	assertCommandExit(t, sessionRun, 0)
	if strings.TrimSpace(sessionRun.Stdout) != "/workspace/session-subdir" {
		t.Fatalf("%s session cwd=%q, want /workspace/session-subdir", mode, strings.TrimSpace(sessionRun.Stdout))
	}
	sessionFile := c.download(t, bedID, "/workspace/session-subdir/from-session.txt")
	if sessionFile.Status != http.StatusOK || string(sessionFile.Body) != "session\n" {
		t.Fatalf("%s session/file API mismatch: status=%d body=%q", mode, sessionFile.Status, sessionFile.Body)
	}

	persisted, response := c.stream(t, sessionPath+"/run", bedID, map[string]any{
		"command": "pwd",
		"timeout": 30_000,
	})
	must2xx(t, mode+" session persisted cwd", response)
	assertCommandExit(t, persisted, 0)
	if strings.TrimSpace(persisted.Stdout) != "/workspace/session-subdir" {
		t.Fatalf("%s persisted session cwd=%q, want /workspace/session-subdir", mode, strings.TrimSpace(persisted.Stdout))
	}

	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	deleted, err := c.json(ctx, "DELETE", sessionPath, bedID, nil, nil)
	cancel()
	if err != nil || deleted.Status != http.StatusOK {
		t.Fatalf("%s delete workspace session: status=%d err=%v body=%s", mode, deleted.Status, err, deleted.Body)
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
