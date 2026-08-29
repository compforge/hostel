//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestRuntimeContract(t *testing.T) {
	target := startTarget(t, targetOptions{isolation: "dorm", maxBeds: 2})
	c := target.client

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var health healthView
	healthResult, err := c.json(ctx, "GET", "/healthz", "", nil, &health)
	if err != nil || healthResult.Status != http.StatusOK {
		t.Fatalf("healthz: status=%d err=%v body=%s", healthResult.Status, err, healthResult.Body)
	}
	if !health.OK || health.MaxBeds != 2 || health.Isolation.Requested != "dorm" || health.Isolation.Effective != "dorm" {
		t.Fatalf("unexpected health contract: %+v", health)
	}

	for _, id := range []string{"contract-a", "contract-b"} {
		created := c.createBed(t, id)
		if created.Status != http.StatusAccepted && created.Status != http.StatusOK {
			t.Fatalf("create %s: status=%d body=%s", id, created.Status, created.Body)
		}
		c.waitBed(t, id, func(b bedView) bool {
			return b.Status.Phase == "resident" && b.Status.Readiness.Ready
		}, "resident and ready")
	}

	inventory := c.waitInventory(t, "two resident idle beds", func(got inventoryView) bool {
		return got.Instance.PhaseCounts["resident"] == 2 && got.Instance.ActivityCount["idle"] == 2
	})
	if inventory.Instance.MaxBeds != 2 || inventory.Instance.Isolation != "dorm" {
		t.Fatalf("unexpected inventory instance: %+v", inventory.Instance)
	}

	third := c.createBed(t, "contract-c")
	if third.Status != http.StatusTooManyRequests {
		t.Fatalf("third bed: status=%d, want 429; body=%s", third.Status, third.Body)
	}
	var capacityError struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(third.Body, &capacityError); err != nil || capacityError.Code != "BED_LIMIT_EXCEEDED" {
		t.Fatalf("third bed error=%+v decode=%v body=%s", capacityError, err, third.Body)
	}

	execResult, response := c.command(t, "contract-a", map[string]any{
		"command": "printf out; printf err >&2; exit 7",
		"timeout": 30_000,
	})
	must2xx(t, "non-zero command", response)
	assertCommandExit(t, execResult, 7)
	if execResult.Stdout != "out" || execResult.Stderr != "err" {
		t.Fatalf("typed command output: stdout=%q stderr=%q", execResult.Stdout, execResult.Stderr)
	}
	if execResult.Result.ExecutorBackend == "" {
		t.Fatalf("command omitted executor backend: %+v", execResult.Result)
	}

	upload := c.upload(t, "contract-a", "/workspace/from-api.txt", []byte("api-seed\n"))
	must2xx(t, "upload file", upload)
	workdir, response := c.command(t, "contract-a", map[string]any{
		"command": "cat from-api.txt && printf command-result > from-command.txt && pwd",
		"cwd":     "/workspace",
		"timeout": 30_000,
	})
	must2xx(t, "workdir command", response)
	assertCommandExit(t, workdir, 0)
	if workdir.Stdout == "" {
		t.Fatal("workdir command returned no stdout")
	}
	download := c.download(t, "contract-a", "/workspace/from-command.txt")
	if download.Status != http.StatusOK || string(download.Body) != "command-result" {
		t.Fatalf("read command artifact: status=%d body=%q", download.Status, download.Body)
	}
	other := c.download(t, "contract-b", "/workspace/from-command.txt")
	if other.Status != http.StatusNotFound {
		t.Fatalf("other bed saw contract-a file: status=%d body=%s", other.Status, other.Body)
	}

	background, response := c.command(t, "contract-a", map[string]any{
		"command":    "sleep 2",
		"background": true,
		"timeout":    10_000,
	})
	must2xx(t, "background command", response)
	if background.ExecutionID == "" {
		t.Fatal("background command omitted execution id")
	}
	c.waitInventory(t, "active contract-a", func(got inventoryView) bool {
		return bedMatches(got, "contract-a", "resident", "active")
	})

	busyCtx, busyCancel := context.WithTimeout(context.Background(), 5*time.Second)
	busy, err := c.json(busyCtx, "DELETE", "/v1/beds/contract-a", "", nil, nil)
	busyCancel()
	if err != nil || busy.Status != http.StatusConflict {
		t.Fatalf("evict active bed: status=%d err=%v body=%s", busy.Status, err, busy.Body)
	}
	var busyError struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(busy.Body, &busyError); err != nil || busyError.Code != "BED_BUSY" {
		t.Fatalf("active eviction error=%+v decode=%v body=%s", busyError, err, busy.Body)
	}

	c.waitInventory(t, "idle contract-a", func(got inventoryView) bool {
		return bedMatches(got, "contract-a", "resident", "idle")
	})
	for _, id := range []string{"contract-a", "contract-b"} {
		purgeCtx, purgeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		result, err := c.json(purgeCtx, "DELETE", "/v1/beds/"+id+"?purge=true", "", nil, nil)
		purgeCancel()
		if err != nil {
			t.Fatalf("purge %s: %v", id, err)
		}
		must2xx(t, "purge "+id, result)
	}
	c.waitInventory(t, "purged beds to disappear", func(got inventoryView) bool {
		return !hasBed(got, "contract-a") && !hasBed(got, "contract-b")
	})
}

func assertCommandExit(t *testing.T, result commandResult, expected int) {
	t.Helper()
	if result.Result == nil || result.Result.Process.Kind != "exited" || result.Result.Process.ExitCode == nil || *result.Result.Process.ExitCode != expected {
		t.Fatalf("command result=%+v, want exited code %d", result.Result, expected)
	}
}

func bedMatches(inventory inventoryView, id, phase, activity string) bool {
	for _, bed := range inventory.Beds {
		if bed.ID == id && bed.Status.Phase == phase && bed.Status.Activity == activity {
			return true
		}
	}
	return false
}

func hasBed(inventory inventoryView, id string) bool {
	for _, bed := range inventory.Beds {
		if bed.ID == id {
			return true
		}
	}
	return false
}
