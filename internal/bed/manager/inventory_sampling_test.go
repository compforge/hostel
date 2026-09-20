package manager

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInventoryReadsCachedDiskHintsAndLiveLifecycle(t *testing.T) {
	m := newTestManager(t)
	defer m.Close(context.Background())
	dir := filepath.Join(m.root, "cold")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	m.sampleLuggage()
	before := m.InventoryStatus()
	if len(before.Beds) != 1 || before.LuggageSampledAt.IsZero() {
		t.Fatalf("sample=%+v", before)
	}
	// Changing disk state must not change query output until background sampling.
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("a much larger value"), 0600); err != nil {
		t.Fatal(err)
	}
	after := m.InventoryStatus()
	if after.Instance.LuggageBytes != before.Instance.LuggageBytes {
		t.Fatal("inventory scanned disk")
	}
	if m.Status().Instance.LuggageBytes != before.Instance.LuggageBytes {
		t.Fatal("status scanned disk")
	}
	m.sampleLuggage()
	if m.InventoryStatus().Instance.LuggageBytes <= before.Instance.LuggageBytes {
		t.Fatal("resampling did not update estimate")
	}
	bed, err := m.Ensure(t.Context(), "live")
	if err != nil {
		t.Fatal(err)
	}
	if bed == nil || m.InventoryStatus().Instance.OccupiedBeds != 1 {
		t.Fatal("live lifecycle was cached with disk hints")
	}
}
