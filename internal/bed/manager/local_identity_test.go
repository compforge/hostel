package manager

import (
	"context"
	"errors"
	model "github.com/qiankunli/hostel/internal/bed"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/bed/privilege"
)

func newIdentityTestManager(t *testing.T, root string) *Manager {
	t.Helper()
	m, err := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := privilege.NewManager(privilege.BedUserReport{Strategy: "per_bed", UIDMin: 200000, UIDMax: 200000}, privilege.CurrentBedUser(), 0, m.owners.Privilege, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.privileges = manager
	m.localIdentities = make(map[string]*localIdentity)
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return m
}
func claimTestUser(t *testing.T, m *Manager, id string) *localIdentity {
	t.Helper()
	m.mu.Lock()
	local := m.localIdentityLocked(id)
	m.mu.Unlock()
	if err := m.privileges.Prepare(context.Background(), local.bed); err != nil {
		t.Fatal(err)
	}
	return local
}
func assertUserReserved(t *testing.T, m *Manager) {
	t.Helper()
	other := model.New("other", "", model.Spec{})
	if err := m.privileges.Prepare(context.Background(), other); err == nil {
		t.Fatal("UID assigned to other while original local data survives")
	}
}
func makeIdentityTree(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name, "data")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "secret"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPurgeRetainsUIDUntilFailedGCCleanupCompletes(t *testing.T) {
	m := newIdentityTestManager(t, t.TempDir())
	claimTestUser(t, m, "same")
	makeIdentityTree(t, m.root, "same")
	fail := true
	m.removeLocalTree = func(path string) error {
		if _, err := os.Stat(path); err == nil && fail {
			return errors.New("injected busy directory")
		}
		return os.RemoveAll(path)
	}
	if m.removeLuggage("same") {
		t.Fatal("GC should retain failed cleanup")
	}
	assertUserReserved(t, m)
	if err := m.Purge(context.Background(), "same"); err == nil {
		t.Fatal("Purge bypassed failed GC cleanup")
	}
	assertUserReserved(t, m)
	if _, err := os.Stat(filepath.Join(m.root, gcTmpPrefix+"same", "data")); err != nil {
		t.Fatal(err)
	}
	reports := m.Status().LocalCleanups
	if len(reports) != 1 || reports[0].Error == "" || reports[0].Running {
		t.Fatalf("cleanup report=%+v", reports)
	}
	fail = false
	if err := m.Purge(context.Background(), "same"); err != nil {
		t.Fatal(err)
	}
	if len(m.Status().LocalCleanups) != 0 {
		t.Fatal("finished cleanup remains reported")
	}
	if err := m.privileges.Prepare(context.Background(), model.New("other", "", model.Spec{})); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryFencesGCResidueWithWatermarksDisabled(t *testing.T) {
	root := t.TempDir()
	makeIdentityTree(t, root, gcTmpPrefix+"same")
	m := newIdentityTestManager(t, root)
	claimTestUser(t, m, "same")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := m.Ensure(ctx, "same"); !errors.Is(err, ErrBedUnavailable) {
		t.Fatalf("create beside recovered cleanup: %v", err)
	}
	assertUserReserved(t, m)
	if len(m.Status().LocalCleanups) != 1 {
		t.Fatal("startup residue is not reported")
	}
	// The normal tick still completes cleanup with high=0 (no cold eviction).
	m.CollectLuggage(ctx)
	if _, err := os.Stat(filepath.Join(root, gcTmpPrefix+"same")); !os.IsNotExist(err) {
		t.Fatalf("GC residue survived: %v", err)
	}
	if err := m.privileges.Prepare(ctx, model.New("other", "", model.Spec{})); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveredGCDoesNotForgetSurvivingColdIdentity(t *testing.T) {
	root := t.TempDir()
	makeIdentityTree(t, root, "same")
	makeIdentityTree(t, root, gcTmpPrefix+"same")
	m := newIdentityTestManager(t, root)
	local := claimTestUser(t, m, "same")
	if err := m.RetryLocalCleanups(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertUserReserved(t, m)
	if _, err := os.Stat(filepath.Join(root, "same", "data", "secret")); err != nil {
		t.Fatal(err)
	}
	// Eviction uses this same terminal cleanup entry after runtime teardown.
	if err := m.cleanLocalIdentity(context.Background(), local, true); err != nil {
		t.Fatal(err)
	}
	if err := m.privileges.Prepare(context.Background(), model.New("other", "", model.Spec{})); err != nil {
		t.Fatal(err)
	}
}

func TestPurgeJoinsRunningGCAndCreateWaitsForCleanup(t *testing.T) {
	m := newIdentityTestManager(t, t.TempDir())
	claimTestUser(t, m, "same")
	makeIdentityTree(t, m.root, "same")
	started, release := make(chan struct{}), make(chan struct{})
	m.removeLocalTree = func(path string) error {
		if _, err := os.Stat(path); err == nil {
			close(started)
			<-release
		}
		return os.RemoveAll(path)
	}
	gcDone := make(chan bool, 1)
	go func() { gcDone <- m.removeLuggage("same") }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, err := m.Ensure(ctx, "same")
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		close(release)
		t.Fatalf("create did not wait: %v", err)
	}
	purgeDone := make(chan error, 1)
	go func() { purgeDone <- m.Purge(context.Background(), "same") }()
	select {
	case err := <-purgeDone:
		close(release)
		t.Fatalf("purge escaped running GC: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	assertUserReserved(t, m)
	close(release)
	if !<-gcDone {
		t.Fatal("GC failed")
	}
	if err := <-purgeDone; err != nil {
		t.Fatal(err)
	}
	if err := m.privileges.Prepare(context.Background(), model.New("other", "", model.Spec{})); err != nil {
		t.Fatal(err)
	}
}

func TestCloseRetriesPendingColdCleanup(t *testing.T) {
	root := t.TempDir()
	makeIdentityTree(t, root, gcTmpPrefix+"same")
	m := newIdentityTestManager(t, root)
	claimTestUser(t, m, "same")
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(m.Status().LocalCleanups) != 0 {
		t.Fatal("Close left cold cleanup pending")
	}
	if err := m.privileges.Prepare(context.Background(), model.New("other", "", model.Spec{})); err != nil {
		t.Fatal(err)
	}
}

func TestStaleIdentityCleanupCannotForgetReplacement(t *testing.T) {
	m := newIdentityTestManager(t, t.TempDir())
	old := claimTestUser(t, m, "same")
	if err := m.cleanLocalIdentity(context.Background(), old, true); err != nil {
		t.Fatal(err)
	}
	next := claimTestUser(t, m, "same")
	makeIdentityTree(t, m.root, "same")
	if old == next {
		t.Fatal("identity was not replaced")
	}
	if err := m.cleanLocalIdentity(context.Background(), old, true); err != nil {
		t.Fatal(err)
	}
	if err := m.privileges.Forget(context.Background(), old.bed); err != nil {
		t.Fatal(err)
	}
	assertUserReserved(t, m)
	if _, err := os.Stat(filepath.Join(m.root, "same", "data", "secret")); err != nil {
		t.Fatal(err)
	}
}
