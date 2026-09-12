package manager

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/qiankunli/hostel/internal/amenity"
	"github.com/qiankunli/hostel/internal/bed/network"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

type failingRelease struct {
	calls   int
	err     error
	revoked bool
}

func (*failingRelease) Name() string                { return "cleanup-test" }
func (*failingRelease) Start(context.Context) error { return nil }
func (*failingRelease) Close(context.Context) error { return nil }
func (*failingRelease) Status() amenity.Status      { return amenity.MCPStatus{State: amenity.StateIdle} }
func (f *failingRelease) NewTenant(context.Context) (amenity.Tenant, error) {
	return &failingTenant{f}, nil
}

type failingTenant struct{ owner *failingRelease }

func (*failingTenant) ID() amenity.TenantID         { return "failure-tenant" }
func (*failingTenant) Status() amenity.TenantStatus { return amenity.MCPTenantStatus{} }
func (t *failingTenant) Close(context.Context) error {
	t.owner.calls++
	t.owner.revoked = true
	return t.owner.err
}

func TestFailedRetirementKeepsIdentityAndDataUntilRetry(t *testing.T) {
	m := newTestManager(t)
	facility := &failingRelease{err: errors.New("upstream unavailable")}
	m.amenities = amenity.NewManager(hostfacts.Collect())
	m.amenities.Register(facility)
	b, err := m.Ensure(context.Background(), "retire")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.amenities.Acquire(t.Context(), b.ID, facility.Name()); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(b.Workspace(), "data")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if evicted, err := m.Evict(context.Background(), b.Name); err == nil || evicted {
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
	if _, err := m.Ensure(context.Background(), b.Name); !errors.Is(err, ErrBedUnavailable) {
		t.Fatalf("reused failed retirement: %v", err)
	}
	if len(m.ListLuggage()) != 0 || m.removeLuggage(b.Name) {
		t.Fatal("pending cleanup exposed to luggage GC")
	}
	if inventory := m.Inventory(); len(inventory) != 1 || inventory[0].Status.Readiness.Reason != "CleanupPending" {
		t.Fatalf("pending cleanup inventory: %+v", inventory)
	}
	facility.err = nil
	if evicted, err := m.Evict(context.Background(), b.Name); err != nil || !evicted {
		t.Fatalf("retry: %t %v", evicted, err)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired data remains: %v", err)
	}
	next, err := m.Ensure(context.Background(), b.Name)
	if err != nil {
		t.Fatal(err)
	}
	if next == b {
		t.Fatal("old resident reused")
	}
	if err := m.rollback(next); err != nil {
		t.Fatal(err)
	}
}

// Only lifecycle methods are used: a replacement must not start while cleanup fails.
// The first network release fails after amenity cleanup succeeds. A retry must
// continue at network, retaining the filesystem until that attachment is gone.
type retryReleaseNetwork struct {
	*network.Manager
	err   error
	calls int
}

func (*retryReleaseNetwork) Status() network.Status { return network.Status{Enabled: true} }
func (n *retryReleaseNetwork) Acquire(context.Context, string) (network.Attachment, error) {
	return n, nil
}
func (n *retryReleaseNetwork) Close(context.Context) error { n.calls++; return n.err }

func TestRetirementResumesAtFailedComponent(t *testing.T) {
	m := newTestManager(t)
	facility := &failingRelease{}
	m.amenities = amenity.NewManager(hostfacts.Collect())
	m.amenities.Register(facility)
	net := &retryReleaseNetwork{err: errors.New("network busy")}
	m.SetNetworkManager(network.WithProvider(net, m.owners.Network))
	b, err := m.Ensure(t.Context(), "retry-components")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.amenities.Acquire(t.Context(), b.ID, facility.Name()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Evict(t.Context(), b.Name); !errors.Is(err, net.err) {
		t.Fatalf("evict: %v", err)
	}
	if facility.calls != 1 || net.calls != 1 {
		t.Fatalf("calls: amenity=%d network=%d", facility.calls, net.calls)
	}
	if _, err := os.Stat(b.Spec().Dir); err != nil {
		t.Fatalf("local identity removed before Release: %v", err)
	}
	net.err = nil
	if _, err := m.Evict(t.Context(), b.Name); err != nil {
		t.Fatal(err)
	}
	if facility.calls != 1 || net.calls != 2 {
		t.Fatalf("replayed successful hooks: amenity=%d network=%d", facility.calls, net.calls)
	}
	if _, err := os.Stat(b.Spec().Dir); !os.IsNotExist(err) {
		t.Fatalf("local identity retained after Release: %v", err)
	}
}

func (*retryReleaseNetwork) Enter(*exec.Cmd) error { return nil }
func (*retryReleaseNetwork) Gateway() string       { return "" }
