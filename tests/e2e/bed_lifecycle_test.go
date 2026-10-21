//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestBedEvictStartsFreshWithNoopStoreAndPurge(t *testing.T) {
	target := startTarget(t, targetOptions{isolation: "dorm", maxBeds: 1})
	c := target.client

	defaultRun, response := c.command(t, "", map[string]any{"command": "printf default-ready", "timeout": 30_000})
	must2xx(t, "run default bed", response)
	assertCommandExit(t, defaultRun, 0)
	inventory := c.inventory(t)
	if hasBed(inventory, "default") {
		t.Fatal("default bed leaked into scheduler inventory")
	}
	if inventory.Instance.Status != "retained" {
		t.Fatalf("instance status = %s, want retained while default bed is resident", inventory.Instance.Status)
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
