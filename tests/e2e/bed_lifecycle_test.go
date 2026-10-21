//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/config"
)

func TestBedEvictStartsFreshWithNoopStoreAndPurge(t *testing.T) {
	target := startTarget(t, targetOptions{isolation: "dorm", maxBeds: 1})
	c := target.client

	inventory := c.inventory(t)
	if len(inventory.Beds) != 0 || inventory.Instance.Status != "releasable" {
		t.Fatalf("new daemon must be empty and releasable: %+v", inventory)
	}

	created := c.createBed(t, "lifecycle-bed")
	if created.Status != http.StatusAccepted && created.Status != http.StatusOK {
		t.Fatalf("create lifecycle bed: status=%d body=%s", created.Status, created.Body)
	}
	c.waitBed(t, "lifecycle-bed", func(b bedView) bool {
		return b.Status.Phase == "resident" && b.Status.Readiness.Ready
	}, "resident and ready")
	upload := c.upload(t, "lifecycle-bed", "/workspace/resume.txt", []byte("survives-eviction"))
	must2xx(t, "seed lifecycle file", upload)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	evicted, err := c.json(ctx, "DELETE", "/v1/beds/lifecycle-bed", "", nil, nil)
	cancel()
	if err != nil || evicted.Status != http.StatusOK {
		t.Fatalf("evict lifecycle bed: status=%d err=%v body=%s", evicted.Status, err, evicted.Body)
	}
	c.waitInventory(t, "evicted Bed removed", func(got inventoryView) bool {
		return !hasBed(got, "lifecycle-bed")
	})

	resumed := c.createBed(t, "lifecycle-bed")
	if resumed.Status != http.StatusAccepted && resumed.Status != http.StatusOK {
		t.Fatalf("resume lifecycle bed: status=%d body=%s", resumed.Status, resumed.Body)
	}
	c.waitBed(t, "lifecycle-bed", func(b bedView) bool {
		return b.Status.Phase == "resident" && b.Status.Readiness.Ready
	}, "resumed and ready")
	artifact := c.download(t, "lifecycle-bed", "/workspace/resume.txt")
	if artifact.Status != http.StatusNotFound {
		t.Fatalf("noop resume retained evicted data: status=%d body=%q", artifact.Status, artifact.Body)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	purged, err := c.json(ctx, "DELETE", "/v1/beds/lifecycle-bed?purge=true", "", nil, nil)
	cancel()
	if err != nil || purged.Status != http.StatusOK {
		t.Fatalf("purge lifecycle bed: status=%d err=%v body=%s", purged.Status, err, purged.Body)
	}
	c.waitInventory(t, "purged lifecycle bed", func(got inventoryView) bool {
		return !hasBed(got, "lifecycle-bed")
	})
}

// +case:id=bed_explicit_pillows,desc=`Reject empty declarations and data access before declaration`,expect=`400/404 without creating a Bed; an explicit Pillow enables command execution`
func TestExplicitBedPillowDeclaration(t *testing.T) {
	c := startTarget(t, targetOptions{isolation: "dorm"}).client
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for _, request := range []map[string]any{{"id": "undeclared"}, {"id": "undeclared", "pillows": nil}, {"id": "undeclared", "pillows": []any{}}} {
		response, err := c.json(ctx, "POST", "/v1/beds", "", request, nil)
		if err != nil || response.Status != 400 {
			t.Fatalf("missing Pillow: status=%d err=%v", response.Status, err)
		}
	}
	_, response := c.command(t, "undeclared", map[string]any{"command": "true"})
	if response.Status != 404 {
		t.Fatalf("access created Bed: %d %s", response.Status, response.Body)
	}
	if hasBed(c.inventory(t), "undeclared") {
		t.Fatal("undeclared Bed entered inventory")
	}
	response, err := c.json(ctx, "POST", "/v1/beds", "", map[string]any{"id": "declared", "pillows": []map[string]any{{"name": "agent"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "declare Pillow", response)
	result, response := c.command(t, "declared", map[string]any{"command": "printf ready"})
	must2xx(t, "execute declared Bed", response)
	assertCommandExit(t, result, 0)
	if result.Stdout != "ready" {
		t.Fatalf("unexpected output: %q", result.Stdout)
	}
}

// +case:id=explicit_bed_idle_release,desc=`Start empty, reject implicit requests, then expire and purge an explicitly declared default Bed`,expect=`All Beds consume capacity and the daemon returns to releasable without a hidden resident`
func TestExplicitBedIdleRelease(t *testing.T) {
	requireTestBinary(t)
	c := startTarget(t, targetOptions{isolation: "dorm", maxBeds: 1, config: &config.Options{BedIdleTTL: value(2 * time.Second)}}).client
	assertEmpty := func(got inventoryView) bool {
		return len(got.Beds) == 0 && got.Instance.Status == "releasable"
	}
	if got := c.inventory(t); !assertEmpty(got) {
		t.Fatalf("new daemon is not empty and releasable: %+v", got)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for _, path := range []string{"/command", "/execd/command"} {
		response, err := c.json(ctx, http.MethodPost, path, "", map[string]any{"command": "true"}, nil)
		if err != nil || response.Status != http.StatusBadRequest {
			t.Fatalf("implicit Bed request %s: status=%d err=%v body=%s", path, response.Status, err, response.Body)
		}
	}
	if got := c.inventory(t); !assertEmpty(got) {
		t.Fatalf("implicit request created a Bed: %+v", got)
	}
	must2xx(t, "declare default", c.createBed(t, "default"))
	c.waitBed(t, "default", func(b bedView) bool { return b.Status.Phase == "resident" && b.Status.Readiness.Ready }, "resident")
	if got := c.inventory(t); len(got.Beds) != 1 || !hasBed(got, "default") || got.Instance.Status != "retained" {
		t.Fatalf("default Bed not visible and retained: %+v", got)
	}
	if response := c.createBed(t, "overflow"); response.Status != http.StatusTooManyRequests {
		t.Fatalf("default Bed bypassed capacity: %d %s", response.Status, response.Body)
	}
	c.waitInventory(t, "idle default Bed reclaimed and instance releasable", assertEmpty)
	must2xx(t, "recreate default", c.createBed(t, "default"))
	result, response := c.command(t, "default", map[string]any{"command": "printf explicit-ready", "timeout": 1000})
	must2xx(t, "explicit command", response)
	assertCommandExit(t, result, 0)
	if result.Stdout != "explicit-ready" {
		t.Fatalf("explicit command output: %q", result.Stdout)
	}
	response, err := c.json(ctx, http.MethodDelete, "/v1/beds/default?purge=true", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "purge default", response)
	c.waitInventory(t, "purged default Bed removed and instance releasable", assertEmpty)
}
