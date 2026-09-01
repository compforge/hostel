//go:build e2e

package e2e_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
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

	wantView := "pathshim"
	if requested == "suite" {
		wantView = "mount"
	}
	if health.WorkspaceView.Mode != wantView || !health.WorkspaceView.Available {
		t.Fatalf("%s isolation is effective, but workspace implementation = %+v, want available %s", requested, health.WorkspaceView, wantView)
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
