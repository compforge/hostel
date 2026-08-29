//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestBedEvictResumeAndPurge(t *testing.T) {
	target := startTarget(t, targetOptions{isolation: "dorm", maxBeds: 1})
	c := target.client

	defaultRun, response := c.command(t, "", map[string]any{"command": "printf default-ready", "timeout": 30_000})
	must2xx(t, "run default bed", response)
	assertCommandExit(t, defaultRun, 0)
	if hasBed(c.inventory(t), "default") {
		t.Fatal("default bed leaked into scheduler inventory")
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
	c.waitInventory(t, "dormant luggage", func(got inventoryView) bool {
		return bedMatches(got, "lifecycle-bed", "dormant", "")
	})

	resumed := c.createBed(t, "lifecycle-bed")
	if resumed.Status != http.StatusAccepted && resumed.Status != http.StatusOK {
		t.Fatalf("resume lifecycle bed: status=%d body=%s", resumed.Status, resumed.Body)
	}
	c.waitBed(t, "lifecycle-bed", func(b bedView) bool {
		return b.Status.Phase == "resident" && b.Status.Readiness.Ready
	}, "resumed and ready")
	artifact := c.download(t, "lifecycle-bed", "/workspace/resume.txt")
	if artifact.Status != http.StatusOK || string(artifact.Body) != "survives-eviction" {
		t.Fatalf("resumed luggage artifact: status=%d body=%q", artifact.Status, artifact.Body)
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
