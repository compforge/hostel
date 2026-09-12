package bed

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/qiankunli/hostel/internal/amenity"
	"github.com/qiankunli/hostel/internal/executor"
	"github.com/qiankunli/hostel/internal/network"
)

type failingRelease struct {
	calls   int
	err     error
	revoked bool
}

func (*failingRelease) Name() string                                         { return "cleanup-test" }
func (*failingRelease) State() string                                        { return amenity.StateRunning }
func (*failingRelease) AcquireTenant(string, string) (amenity.Tenant, error) { return nil, nil }
func (f *failingRelease) ReleaseTenant(string) error                         { f.calls++; return f.err }
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

// The first network release fails after amenity cleanup succeeds. A retry must
// continue at network, retaining the filesystem until that attachment is gone.
type retryReleaseNetwork struct {
	*network.Manager
	err   error
	calls int
}

func (*retryReleaseNetwork) Diagnostics() network.Report { return network.Report{Enabled: true} }
func (n *retryReleaseNetwork) Acquire(context.Context, string) (network.Attachment, error) {
	return n, nil
}
func (n *retryReleaseNetwork) Close(context.Context) error { n.calls++; return n.err }

func TestRetirementResumesAtFailedComponent(t *testing.T) {
	m := newTestManager(t)
	facility := &failingRelease{}
	m.amenities = amenity.NewRegistry()
	m.amenities.Register(facility)
	net := &retryReleaseNetwork{err: errors.New("network busy")}
	m.network = net
	b, err := m.Ensure(t.Context(), "retry-components")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Evict(t.Context(), b.ID); !errors.Is(err, net.err) {
		t.Fatalf("evict: %v", err)
	}
	if facility.calls != 1 || net.calls != 1 {
		t.Fatalf("calls: amenity=%d network=%d", facility.calls, net.calls)
	}
	if _, err := os.Stat(b.Dir); err != nil {
		t.Fatalf("local identity removed before Release: %v", err)
	}
	net.err = nil
	if _, err := m.Evict(t.Context(), b.ID); err != nil {
		t.Fatal(err)
	}
	if facility.calls != 1 || net.calls != 2 {
		t.Fatalf("replayed successful hooks: amenity=%d network=%d", facility.calls, net.calls)
	}
	if _, err := os.Stat(b.Dir); !os.IsNotExist(err) {
		t.Fatalf("local identity retained after Release: %v", err)
	}
}

func (*retryReleaseNetwork) Enter(*exec.Cmd) error { return nil }
func (*retryReleaseNetwork) Gateway() string       { return "" }
