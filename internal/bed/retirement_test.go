package bed

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/qiankunli/hostel/internal/amenity"
	"github.com/qiankunli/hostel/internal/executor"
)

type failingRelease struct {
	err     error
	revoked bool
}

func (*failingRelease) Name() string                                         { return "cleanup-test" }
func (*failingRelease) State() string                                        { return amenity.StateRunning }
func (*failingRelease) AcquireTenant(string, string) (amenity.Tenant, error) { return nil, nil }
func (f *failingRelease) ReleaseTenant(string) error                         { return f.err }
func (f *failingRelease) RevokeBedSecrets(string)                            { f.revoked = true }

func TestFailedRetirementKeepsIdentityAndDataUntilRetry(t *testing.T) {
	m := newTestManager(t)
	facility := &failingRelease{err: errors.New("upstream unavailable")}
	m.amenities = amenity.NewRegistry()
	m.amenities.Register(facility)
	b, err := m.Ensure(context.Background(), "retire")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(b.Workspace(), "data")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if evicted, err := m.Evict(context.Background(), b.ID); err == nil || evicted {
		t.Fatalf("cleanup failure swallowed: %t %v", evicted, err)
	}
	if m.OccupiedBedCount() != 1 {
		t.Fatal("pending cleanup released capacity")
	}
	if !facility.revoked {
		t.Fatal("failed cleanup retained Bed credentials")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("data deleted before cleanup: %v", err)
	}
	if _, err := m.Ensure(context.Background(), b.ID); !errors.Is(err, ErrBedUnavailable) {
		t.Fatalf("reused failed retirement: %v", err)
	}
	if len(m.ListLuggage()) != 0 || m.removeLuggage(b.ID) {
		t.Fatal("pending cleanup exposed to luggage GC")
	}
	if inventory := m.Inventory(); len(inventory) != 1 || inventory[0].Status.Readiness.Reason != "CleanupPending" {
		t.Fatalf("pending cleanup inventory: %+v", inventory)
	}
	facility.err = nil
	if evicted, err := m.Evict(context.Background(), b.ID); err != nil || !evicted {
		t.Fatalf("retry: %t %v", evicted, err)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired data remains: %v", err)
	}
	next, err := m.Ensure(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next == b {
		t.Fatal("old resident reused")
	}
	if err := m.teardown(next); err != nil {
		t.Fatal(err)
	}
}

// Only lifecycle methods are used: a replacement must not start while cleanup fails.
type stalledExecutor struct {
	executor.Executor
	err error
}

func (*stalledExecutor) State() executor.State            { return executor.StateLost }
func (e *stalledExecutor) Shutdown(context.Context) error { return e.err }

type replacementFactory struct {
	executor.Factory
	created int
	next    executor.Executor
}

func (f *replacementFactory) Create(context.Context, string) (executor.Executor, error) {
	f.created++
	return f.next, nil
}
func TestExecutorReplacementWaitsForCleanup(t *testing.T) {
	old := &stalledExecutor{err: errors.New("cleanup busy")}
	next := &stalledExecutor{}
	b := &Bed{ID: "replacement", executor: old}
	factory := &replacementFactory{next: next}
	if _, err := b.executorFor(context.Background(), factory); !errors.Is(err, old.err) || factory.created != 0 {
		t.Fatalf("replaced before cleanup: %v creates=%d", err, factory.created)
	}
	old.err = nil
	got, err := b.executorFor(context.Background(), factory)
	if err != nil || got != next || factory.created != 1 {
		t.Fatalf("retry replacement: %v creates=%d", err, factory.created)
	}
}
