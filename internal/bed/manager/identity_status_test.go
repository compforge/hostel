package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/bed/network"
)

func TestBedNameLocalLifetimeAndRecovery(t *testing.T) {
	ctx := t.Context()
	m := newTestManager(t)
	b, err := m.Ensure(ctx, "中文客户-1")
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "中文客户-1" || b.ID.String() == b.Name || b.Bed != b.local.bed {
		t.Fatalf("identity boundary: id=%s name=%s same_object=%t", b.ID, b.Name, b.Bed == b.local.bed)
	}
	if !b.Bed.Status().Privilege.Assigned {
		t.Fatal("missing privilege state")
	}
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	m2, err := NewManager(m.root, "default", "/bin/bash", isolation.New("dorm", m.root), nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close(context.Background())
	recovered, err := m2.Ensure(ctx, b.Name)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.ID != b.ID || recovered.Bed != recovered.local.bed {
		t.Fatal("restart replaced local identity")
	}
	if _, err := m2.Evict(ctx, recovered.Name); err != nil {
		t.Fatal(err)
	}
	if recovered.Bed.Status().Privilege.Assigned {
		t.Fatal("Forget left the published privilege assigned")
	}
	if _, err := os.Stat(m2.identityPath(recovered.Name)); !os.IsNotExist(err) {
		t.Fatalf("forgotten record retained: %v", err)
	}
	next, err := m2.Ensure(ctx, recovered.Name)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID == recovered.ID || next.Bed == recovered.Bed {
		t.Fatal("new lifetime reused forgotten identity")
	}
	if err := m2.privileges.Forget(ctx, recovered.Bed); err != nil {
		t.Fatal(err)
	}
	if !next.Bed.Status().Privilege.Assigned {
		t.Fatal("stale Forget changed replacement")
	}
}

func TestRetainedBedReinitializationReusesModel(t *testing.T) {
	m := newTestManager(t)
	b, err := m.Ensure(t.Context(), "retry")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(context.Background())
	if err := m.rollback(b); err != nil {
		t.Fatal(err)
	}
	// A stopped runtime can be reconstructed without forgetting local data.
	m.mu.Lock()
	delete(m.beds, b.Name)
	m.residentBeds.Add(-1)
	m.mu.Unlock()
	next, err := m.Ensure(t.Context(), b.Name)
	if err != nil {
		t.Fatal(err)
	}
	if next.Bed != b.Bed || next.ID != b.ID {
		t.Fatal("reinitialization replaced durable model")
	}
	if !next.Bed.Status().Filesystem.Prepared || !next.Bed.Status().Lifecycle.Ready {
		t.Fatal("reinitialized state missing")
	}
}

func TestExecutorExitPublishesSharedStatus(t *testing.T) {
	m := newTestManager(t)
	defer m.Close(context.Background())
	b, err := m.Ensure(t.Context(), "executor-status")
	if err != nil {
		t.Fatal(err)
	}
	e, err := m.executorManager.For(t.Context(), b.Bed)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for b.Bed.Status().Executor.State != string(executor.StateStopped) {
		if time.Now().After(deadline) {
			t.Fatalf("stale shared status: %+v", b.Bed.Status().Executor)
		}
		time.Sleep(time.Millisecond)
	}
	next, err := m.executorManager.For(t.Context(), b.Bed)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID() == e.ID() || b.Bed.Status().Executor.ID != next.ID() {
		t.Fatal("replacement status missing")
	}
}

type deadlineNetwork struct {
	*network.Manager
	mu        sync.Mutex
	deadlines []time.Time
	release   bool
}
type deadlineAttachment struct{ owner *deadlineNetwork }

func (*deadlineNetwork) Status() network.Status { return network.Status{Enabled: true} }
func (n *deadlineNetwork) Acquire(context.Context, string) (network.Attachment, error) {
	return &deadlineAttachment{n}, nil
}
func (*deadlineNetwork) Close(context.Context) error { return nil }
func (*deadlineAttachment) Enter(*exec.Cmd) error    { return nil }
func (*deadlineAttachment) Gateway() string          { return "" }
func (a *deadlineAttachment) Close(ctx context.Context) error {
	n := a.owner
	n.mu.Lock()
	deadline, _ := ctx.Deadline()
	n.deadlines = append(n.deadlines, deadline)
	release := n.release
	n.mu.Unlock()
	if release {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestCloseUsesOneDeadlineAndRetainsCleanupOwners(t *testing.T) {
	m := newTestManager(t)
	n := &deadlineNetwork{}
	m.SetNetworkManager(network.WithProvider(n, m.owners.Network))
	for i := range 3 {
		if _, err := m.Ensure(t.Context(), fmt.Sprint("bed-", i)); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	started := time.Now()
	if err := m.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close: %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("per-bed cleanup exceeded total shutdown budget")
	}
	n.mu.Lock()
	for _, observed := range n.deadlines {
		if observed.After(deadline) {
			t.Errorf("cleanup deadline extended: %s > %s", observed, deadline)
		}
	}
	n.release = true
	n.mu.Unlock()
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, b := range m.List() {
		if b.Bed.Status().Filesystem.Prepared {
			t.Fatal("retry left filesystem prepared")
		}
	}
}

func TestCloseDeadlineWhileAnotherCleanupOwnsBed(t *testing.T) {
	m := newTestManager(t)
	b, err := m.Ensure(t.Context(), "owned")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.cleanupMu.Acquire(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if err := m.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close waiting for owner: %v", err)
	}
	b.cleanupMu.Release(1)
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestInventoryStatusCountsSameCapturedRows(t *testing.T) {
	m := newTestManager(t)
	defer m.Close(context.Background())
	for _, name := range []string{"a", "b"} {
		if _, err := m.Ensure(t.Context(), name); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := m.Get("a")
	finish, err := m.BeginOperation(b, OpFile, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	status := m.Status()
	if status.Instance.OccupiedBeds != len(status.Beds) || status.Instance.ResidentBeds != 2 || status.Instance.PinnedBeds != 1 || status.Instance.ActivityCounts["active"] != 1 || status.Instance.PhaseCounts["resident"] != 2 {
		t.Fatalf("inconsistent inventory: %+v", status.InventoryStatus)
	}
	if status.Instance.Status != InstanceRetained {
		t.Fatal(status.Instance.Status)
	}
}

func TestLocalIDIsNotInsidePortableBedTree(t *testing.T) {
	m := newTestManager(t)
	defer m.Close(context.Background())
	b, err := m.Ensure(t.Context(), "portable")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(b.Spec().Dir, m.identityPath(b.Name))
	if err != nil || filepath.IsLocal(rel) {
		t.Fatalf("local identity travels with BedFS: %s %v", rel, err)
	}
	if got := model.ID(b.ID.String()); got != b.ID {
		t.Fatal("ID encoding changed")
	}
}

func TestStartupRemovesIdentityReservedBeforeBedFS(t *testing.T) {
	m := newTestManager(t)
	local := m.localIdentityLocked("never-prepared")
	if err := m.saveLocalIdentity(local); err != nil {
		t.Fatal(err)
	}
	m.localIdentities = make(map[string]*localIdentity)
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer m.Close(context.Background())
	if _, err := os.Stat(m.identityPath("never-prepared")); !os.IsNotExist(err) {
		t.Fatalf("orphan record remains: %v", err)
	}
}
