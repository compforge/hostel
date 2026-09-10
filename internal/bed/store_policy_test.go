package bed

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/isolation"
	"github.com/qiankunli/hostel/internal/store"
)

type countedPolicyStore struct {
	*fakeStore
	callsMu sync.Mutex
	calls   []string
}

func (s *countedPolicyStore) record(op, id string) {
	s.callsMu.Lock()
	defer s.callsMu.Unlock()
	s.calls = append(s.calls, op+":"+id)
}
func (s *countedPolicyStore) Stat(ctx context.Context, id string) (*store.SnapshotInfo, error) {
	s.record("stat", id)
	return s.fakeStore.Stat(ctx, id)
}
func (s *countedPolicyStore) Restore(ctx context.Context, id, dir string) error {
	s.record("restore", id)
	return s.fakeStore.Restore(ctx, id, dir)
}
func (s *countedPolicyStore) Persist(ctx context.Context, id, dir string, generation int64) error {
	s.record("persist", id)
	return s.fakeStore.Persist(ctx, id, dir, generation)
}
func (s *countedPolicyStore) Delete(ctx context.Context, id string) error {
	s.record("delete", id)
	return s.fakeStore.Delete(ctx, id)
}

func policyTestManager(t *testing.T, root string, st store.Store) *Manager {
	t.Helper()
	m, err := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, st)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMixedBedStoresKeepNoopOutOfAllBackendIO(t *testing.T) {
	ctx := context.Background()
	st := &countedPolicyStore{fakeStore: newFakeStore()}
	// Even an old remote snapshot with the same ID must not be touched by an
	// explicitly noop Bed on a fresh carrier.
	st.snaps["ephemeral"] = []byte("old remote contents")
	m := policyTestManager(t, t.TempDir(), st)
	defer m.Close(ctx)
	if _, err := m.InitializeBedWithOptions(ctx, "ephemeral", CreateOptions{Store: StoreNoop}); err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(ctx, "ephemeral")
	if err != nil {
		t.Fatal(err)
	}
	if b.StoreName() != "noop" || b.durable {
		t.Fatalf("noop bed store=%s durable=%v", b.StoreName(), b.durable)
	}
	if err := os.WriteFile(filepath.Join(b.Workspace(), "marker.txt"), []byte("local only"), 0600); err != nil {
		t.Fatal(err)
	}
	finish, err := m.BeginOperation(b, OpFile, 0)
	if err != nil {
		t.Fatal(err)
	}
	finish()
	if !b.Status().DataSynced || b.Status().Pinned {
		t.Fatal("noop dirtiness must not pin durability")
	}
	if err := m.Checkpoint(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	normal, err := m.Ensure(ctx, "normal")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(normal.Workspace(), "marker.txt"), []byte("durable"), 0600); err != nil {
		t.Fatal(err)
	}
	finish, err = m.BeginOperation(normal, OpFile, 0)
	if err != nil {
		t.Fatal(err)
	}
	finish()
	if err := m.Checkpoint(ctx, normal.ID); err != nil {
		t.Fatal(err)
	}
	for _, entry := range m.Inventory() {
		if entry.ID == b.ID && entry.Store != "noop" {
			t.Fatalf("inventory=%+v", entry)
		}
	}
	for _, id := range []string{b.ID, normal.ID} {
		if ok, err := m.Evict(ctx, id); err != nil || !ok {
			t.Fatalf("evict %s: %v %v", id, ok, err)
		}
	}
	resumed, err := m.Ensure(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.StoreName() != "noop" {
		t.Fatal("evict lost store policy")
	}
	if _, err := os.Stat(filepath.Join(resumed.Workspace(), "marker.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("noop restored contents: %v", err)
	}
	durable, err := m.Ensure(ctx, normal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(durable.Workspace(), "restored.txt")); err != nil {
		t.Fatalf("default bed did not restore: %v", err)
	}
	if err := m.Purge(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	st.callsMu.Lock()
	defer st.callsMu.Unlock()
	for _, call := range st.calls {
		if call == "stat:ephemeral" || call == "restore:ephemeral" || call == "persist:ephemeral" || call == "delete:ephemeral" {
			t.Fatalf("noop reached default backend: %s", call)
		}
	}
}

func TestBedStoreSelectionSurvivesRestartAndPurgeResetsIdentity(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st := newFakeStore()
	first := policyTestManager(t, root, st)
	if _, err := first.InitializeBedWithOptions(ctx, "one", CreateOptions{Store: StoreNoop}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Ensure(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}
	second := policyTestManager(t, root, st)
	defer second.Close(ctx)
	b, err := second.Ensure(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if b.StoreName() != "noop" {
		t.Fatal("restart lost selection")
	}
	if _, err := second.InitializeBedWithOptions(ctx, "one", CreateOptions{Store: StoreDefault}); !errors.Is(err, ErrStoreConflict) {
		t.Fatalf("store switch error=%v", err)
	}
	if err := second.Purge(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	b, err = second.Ensure(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if b.StoreName() != st.Name() {
		t.Fatal("purge did not end the previous store identity")
	}
}

func TestBedStoreConflictWhileInitializing(t *testing.T) {
	ctx := context.Background()
	st := &initializationBlockingStore{fakeStore: newFakeStore(), started: make(chan struct{}, 1), release: make(chan struct{})}
	m := policyTestManager(t, t.TempDir(), st)
	defer m.Close(ctx)
	if _, err := m.InitializeBed(ctx, "pending"); err != nil {
		t.Fatal(err)
	}
	<-st.started
	_, err := m.InitializeBedWithOptions(ctx, "pending", CreateOptions{Store: StoreNoop})
	close(st.release)
	if !errors.Is(err, ErrStoreConflict) {
		t.Fatalf("concurrent store switch=%v", err)
	}
	if _, err := m.Ensure(ctx, "pending"); err != nil {
		t.Fatal(err)
	}
}

func TestNoopOrphanGCDoesNotInspectDefaultStore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st := &countedPolicyStore{fakeStore: newFakeStore()}
	m := policyTestManager(t, root, st)
	if err := m.saveStorePolicy("orphan", StoreNoop); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "orphan")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveMeta(dir, bedMeta{Version: 1, BedID: "orphan"}); err != nil {
		t.Fatal(err)
	}
	m.SetLuggageLimits(1, 0)
	if got := m.CollectLuggage(ctx); len(got) != 1 {
		t.Fatalf("collected=%v", got)
	}
	b, err := m.Ensure(ctx, "orphan")
	if err != nil {
		t.Fatal(err)
	}
	if b.StoreName() != "noop" {
		t.Fatal("GC lost policy")
	}
	if err := m.Purge(ctx, "orphan"); err != nil {
		t.Fatal(err)
	}
	if len(st.calls) != 0 {
		t.Fatalf("noop GC/recreate/purge touched backend: %v", st.calls)
	}
}

func TestCorruptStorePolicyFailsWithoutDefaultIO(t *testing.T) {
	m := policyTestManager(t, t.TempDir(), newFakeStore())
	if err := os.MkdirAll(filepath.Dir(m.storePolicyPath("one")), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.storePolicyPath("one"), []byte(`{"store":"unknown"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(context.Background(), "one"); !errors.Is(err, ErrStoreRecord) {
		t.Fatalf("corrupt selection=%v", err)
	}
}

func TestOmittedBedStoreInheritsConfiguredBackend(t *testing.T) {
	ctx := context.Background()
	st := &countedPolicyStore{fakeStore: newFakeStore()}
	m := policyTestManager(t, t.TempDir(), st)
	b, err := m.Ensure(ctx, "unspecified")
	if err != nil {
		t.Fatal(err)
	}
	if b.StoreName() != st.Name() {
		t.Fatalf("unspecified store=%s", b.StoreName())
	}
	if err := m.Checkpoint(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.Purge(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if len(st.calls) == 0 {
		t.Fatal("unspecified Bed did not use configured backend")
	}
}

func TestDurableBedSyncsWithNoopInstanceDefault(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := policyTestManager(t, t.TempDir(), store.Noop{})
	b, err := m.Ensure(ctx, "durable")
	if err != nil {
		t.Fatal(err)
	}
	// Install the selected backend before starting the controller; the unit
	// fixture avoids external S3 while keeping a noop instance default.
	st := newFakeStore()
	b.store = st
	b.storePolicy = st.Name()
	b.durable = true
	if err := os.WriteFile(filepath.Join(b.Workspace(), "marker.txt"), []byte("sync"), 0600); err != nil {
		t.Fatal(err)
	}
	finish, err := m.BeginOperation(b, OpFile, 0)
	if err != nil {
		t.Fatal(err)
	}
	finish()
	done := make(chan struct{})
	go func() { defer close(done); m.RunStoreSync(ctx, 0) }()
	defer func() { cancel(); <-done; _ = m.Close(context.Background()) }()
	m.RequestStoreSync()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		st.mu.Lock()
		_, persisted := st.snaps[b.ID]
		st.mu.Unlock()
		if persisted {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("instance noop default disabled the durable Bed sync")
}
