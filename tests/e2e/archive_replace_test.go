//go:build e2e

package e2e_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConcurrentUnzipReplaceAcrossBeds(t *testing.T) {
	for _, requested := range []string{"dorm", "room", "suite"} {
		requested := requested
		t.Run(requested, func(t *testing.T) {
			target := startTarget(t, targetOptions{isolation: requested, maxBeds: 8})
			c := target.client
			health := requireSupportedArchiveReplaceView(t, c, requested)

			probe, response := c.command(t, "", map[string]any{
				"command": "command -v unzip >/dev/null",
				"timeout": 30_000,
			})
			must2xx(t, "probe unzip", response)
			if probe.Result == nil || probe.Result.Process.ExitCode == nil || *probe.Result.Process.ExitCode != 0 {
				t.Skipf("target for %s does not provide unzip", requested)
			}

			t.Logf("requested=%s effective=%s workspace_view=%s", requested, health.Isolation.Effective, health.WorkspaceView.Mode)
			type bedCase struct {
				id       string
				expected string
			}
			beds := make([]bedCase, 0, 8)
			for i := 0; i < 8; i++ {
				bedID := fmt.Sprintf("unzip-%s-%d", requested, i)
				expected := fmt.Sprintf("content-from-%s", bedID)
				created := c.createBed(t, bedID)
				if created.Status != http.StatusAccepted && created.Status != http.StatusOK {
					t.Fatalf("create %s: status=%d body=%s", bedID, created.Status, created.Body)
				}
				c.waitBed(t, bedID, func(b bedView) bool {
					return b.Status.Phase == "resident" && b.Status.Readiness.Ready
				}, "resident and ready")
				archive := concurrentReplaceArchive(t, expected)
				must2xx(t, "upload unzip archive", c.upload(t, bedID, "/workspace/.concurrent-replace.zip", archive))
				beds = append(beds, bedCase{id: bedID, expected: expected})
			}
			for _, bed := range beds {
				bed := bed
				t.Run(bed.id, func(t *testing.T) {
					t.Parallel()

					run, response := c.command(t, bed.id, map[string]any{
						"command": "i=0; while [ \"$i\" -lt 25 ]; do unzip -oq /workspace/.concurrent-replace.zip -d /workspace/skills; i=$((i + 1)); done",
						"timeout": 60_000,
					})
					must2xx(t, "replace existing zip entries", response)
					if run.Result == nil || run.Result.Process.Kind != "exited" || run.Result.Process.ExitCode == nil || *run.Result.Process.ExitCode != 0 {
						t.Fatalf("concurrent unzip result=%+v stdout=%q stderr=%q", run.Result, run.Stdout, run.Stderr)
					}

					artifact := c.download(t, bed.id, "/workspace/skills/stock-analysis-router/references/dimension-tools.md")
					if artifact.Status != http.StatusOK || string(artifact.Body) != bed.expected {
						t.Fatalf("bed-local unzip result: status=%d body=%q, want %q", artifact.Status, artifact.Body, bed.expected)
					}
				})
			}
		})
	}
}

func TestDormPathshimFailureUsesProotWithoutEscapingBedFS(t *testing.T) {
	if strings.TrimSpace(os.Getenv(imageEnv)) == "" {
		t.Skip("requires image mode so the test owns the carrier /workspace root")
	}

	target := startTarget(t, targetOptions{
		isolation:        "dorm",
		maxBeds:          4,
		allowPtrace:      true,
		pathshimHostPath: unavailablePathshim(t),
		workspaceRoot:    "/workspace",
	})
	c := target.client
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	var health healthView
	result, err := c.json(ctx, "GET", "/healthz", "", nil, &health)
	cancel()
	if err != nil || result.Status != http.StatusOK {
		t.Fatalf("healthz: status=%d err=%v body=%s", result.Status, err, result.Body)
	}
	if health.Isolation.Effective != "dorm" || health.WorkspaceView.Mode != "proot" || !health.WorkspaceView.Available {
		t.Fatalf("fault injection did not activate dorm PRoot fallback: isolation=%+v workspace_view=%+v", health.Isolation, health.WorkspaceView)
	}
	t.Logf("fault active: workspace_view=%s available=%t", health.WorkspaceView.Mode, health.WorkspaceView.Available)

	type bedCase struct {
		id       string
		expected string
		execID   string
	}
	beds := make([]bedCase, 0, 4)
	for i := 0; i < 4; i++ {
		bedID := fmt.Sprintf("fallback-unzip-%d", i)
		expected := fmt.Sprintf("content-from-%s", bedID)
		created := c.createBed(t, bedID)
		if created.Status != http.StatusAccepted && created.Status != http.StatusOK {
			t.Fatalf("create %s: status=%d body=%s", bedID, created.Status, created.Body)
		}
		c.waitBed(t, bedID, func(b bedView) bool {
			return b.Status.Phase == "resident" && b.Status.Readiness.Ready
		}, "resident and ready")

		beds = append(beds, bedCase{id: bedID, expected: expected})
	}

	for i := range beds {
		must2xx(t, "upload fallback archive", c.upload(t, beds[i].id, "/workspace/.fallback-resources.zip", concurrentReplaceArchive(t, beds[i].expected)))

		// Exercise the customer's replace-heavy workload through PRoot, including
		// child exec, rename/unlink, repeated overwrite, and concurrent Beds.
		run, response := c.command(t, beds[i].id, map[string]any{
			"command":    "mkdir -p /workspace/skills; i=0; while [ \"$i\" -lt 25 ]; do unzip -oq /workspace/.fallback-resources.zip -d /workspace/skills; i=$((i + 1)); done",
			"cwd":        "/workspace",
			"background": true,
			"timeout":    60_000,
		})
		must2xx(t, "start fallback unzip", response)
		if run.ExecutionID == "" {
			t.Fatalf("fallback unzip for %s omitted execution id", beds[i].id)
		}
		beds[i].execID = run.ExecutionID
	}

	for _, bed := range beds {
		finished := c.waitExecution(t, bed.execID, func(status executionStatusView) bool {
			return !status.Running && status.Result != nil
		}, "finish fallback unzip")
		if finished.Result.Process.Kind != "exited" || finished.Result.Process.ExitCode == nil || *finished.Result.Process.ExitCode != 0 {
			t.Errorf("PRoot fallback command failed for %s: %+v", bed.id, finished.Result.Process)
			continue
		}
		artifact := c.download(t, bed.id, "/workspace/skills/stock-analysis-router/references/dimension-tools.md")
		if artifact.Status != http.StatusOK || string(artifact.Body) != bed.expected {
			t.Errorf("successful PRoot fallback command escaped BedFS for %s: status=%d body=%q, want %q", bed.id, artifact.Status, artifact.Body, bed.expected)
		}
	}

	if inventory := c.inventory(t); hasBed(inventory, "skills") {
		t.Fatalf("PRoot fallback created carrier-root ghost Bed %q: %+v", "skills", inventory.Beds)
	}
}

func unavailablePathshim(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pathshim")
	const script = `#!/bin/sh
if [ "${1:-}" = "probe" ]; then
  printf 'passthrough\n'
  echo 'pathshim: probe mode=passthrough reason=bind-view-unavailable error=Invalid argument (os error 22)' >&2
  exit 1
fi
echo 'pathshim: unexpected command invocation after failed startup probe' >&2
exit 99
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write unavailable pathshim: %v", err)
	}
	return path
}

func requireSupportedArchiveReplaceView(t *testing.T, c *apiClient, requested string) healthView {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var health healthView
	result, err := c.json(ctx, "GET", "/healthz", "", nil, &health)
	if err != nil || result.Status != http.StatusOK {
		t.Fatalf("healthz: status=%d err=%v body=%s", result.Status, err, result.Body)
	}
	if health.Isolation.Effective != requested {
		if requiredIsolationLevels()[requested] {
			t.Fatalf("test environment promised isolation %s but host degraded it to %s (ceiling=%s mechanism=%s)", requested, health.Isolation.Effective, health.Isolation.Ceiling, health.Isolation.Mechanism)
		}
		t.Skipf("host capability unavailable: requested isolation %s degraded to %s (ceiling=%s mechanism=%s)", requested, health.Isolation.Effective, health.Isolation.Ceiling, health.Isolation.Mechanism)
	}

	if requested == "suite" {
		if health.WorkspaceView.Mode != "mount" || !health.WorkspaceView.Available {
			t.Fatalf("%s isolation is effective, but workspace implementation = %+v, want available mount", requested, health.WorkspaceView)
		}
	} else if (health.WorkspaceView.Mode != "pathshim" && health.WorkspaceView.Mode != "proot") || !health.WorkspaceView.Available {
		t.Fatalf("%s isolation is effective, but workspace implementation = %+v, want an available user-space view", requested, health.WorkspaceView)
	}
	return health
}

func concurrentReplaceArchive(t *testing.T, expected string) []byte {
	t.Helper()
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	for i := 0; i < 64; i++ {
		name := fmt.Sprintf("stock-analysis-router/references/filler-%02d.txt", i)
		content := strings.Repeat(fmt.Sprintf("%s-%02d\n", expected, i), 64)
		if i == 0 {
			name = "stock-analysis-router/references/dimension-tools.md"
			content = expected
		}
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		entry, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip archive: %v", err)
	}
	return archive.Bytes()
}
