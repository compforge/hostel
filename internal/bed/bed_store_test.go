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

type countedStore struct {
	*fakeStore
	callsMu sync.Mutex
	calls   []string
}

func (s *countedStore) record(op, id string) {
	s.callsMu.Lock()
	defer s.callsMu.Unlock()
	s.calls = append(s.calls, op+":"+id)
}
func (s *countedStore) Stat(ctx context.Context, id string) (*store.SnapshotInfo, error) {
	s.record("stat", id)
	return s.fakeStore.Stat(ctx, id)
}
func (s *countedStore) Restore(ctx context.Context, id, dir string) error {
	s.record("restore", id)
	return s.fakeStore.Restore(ctx, id, dir)
}
func (s *countedStore) Persist(ctx context.Context, id, dir string, generation int64) error {
	s.record("persist", id)
	return s.fakeStore.Persist(ctx, id, dir, generation)
}
func (s *countedStore) Delete(ctx context.Context, id string) error {
	s.record("delete", id)
	return s.fakeStore.Delete(ctx, id)
}

func storeTestManager(t *testing.T, root string, st store.Store) *Manager {
	t.Helper()
	m, err := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, st)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMixedBedStoresKeepNoopOutOfAllBackendIO(t *testing.T) {
	ctx := context.Background()
	st := &countedStore{fakeStore: newFakeStore()}
	// Even an old remote snapshot with the same ID must not be touched by an
	// explicitly noop Bed on a fresh carrier.
	st.snaps["ephemeral"] = []byte("old remote contents")
	m := storeTestManager(t, t.TempDir(), st)
	defer m.Close(ctx)
	if _, err := m.InitializeBedWithOptions(ctx, "ephemeral", CreateOptions{Store: "noop"}); err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(ctx, "ephemeral")
	if err != nil {
		t.Fatal(err)
	}
	if b.StoreName() != "noop" {
		t.Fatalf("noop bed store=%s", b.StoreName())
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
	if _, err := m.InitializeBedWithOptions(ctx, b.ID, CreateOptions{Store: "noop"}); err != nil {
		t.Fatal(err)
	}
	resumed, err := m.Ensure(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.StoreName() != "noop" {
		t.Fatal("recreate ignored explicit store")
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

func TestBedStoreUsesLocalMetadataAndDefaultsAfterEviction(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dir := filepath.Join(root, "one")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// An orphaned working copy retains its own Store across a daemon restart.
	if err := saveMeta(dir, bedMeta{Version: 1, BedID: "one", Store: "noop"}); err != nil {
		t.Fatal(err)
	}
	st := newFakeStore()
	m := storeTestManager(t, root, st)
	defer m.Close(ctx)
	b, err := m.Ensure(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if b.StoreName() != "noop" {
		t.Fatal("local metadata lost selection")
	}
	if _, err := m.InitializeBedWithOptions(ctx, "one", CreateOptions{Store: st.Name()}); !errors.Is(err, ErrStoreConflict) {
		t.Fatalf("live store switch=%v", err)
	}
	if ok, err := m.Evict(ctx, "one"); err != nil || !ok {
		t.Fatalf("evict=%v %v", ok, err)
	}
	b, err = m.Ensure(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if b.StoreName() != st.Name() {
		t.Fatal("absent Bed without override must inherit default")
	}
}

func TestPurgeAbsentBedUsesAPIStore(t *testing.T) {
	ctx := context.Background()
	st := &countedStore{fakeStore: newFakeStore()}
	m := storeTestManager(t, t.TempDir(), st)
	defer m.Close(ctx)
	// Caller-managed data with the same ID must be untouched even without local metadata.
	st.snaps["absent"] = []byte("external data")
	if err := m.PurgeWithStore(ctx, "absent", "noop"); err != nil {
		t.Fatal(err)
	}
	if len(st.calls) != 0 {
		t.Fatalf("noop purge reached default store: %v", st.calls)
	}
	if err := m.Purge(ctx, "absent"); err != nil {
		t.Fatal(err)
	}
	if len(st.calls) != 1 || st.calls[0] != "delete:absent" {
		t.Fatalf("default purge calls=%v", st.calls)
	}
}

func TestBedStoreConflictWhileInitializing(t *testing.T) {
	ctx := context.Background()
	st := &initializationBlockingStore{fakeStore: newFakeStore(), started: make(chan struct{}, 1), release: make(chan struct{})}
	m := storeTestManager(t, t.TempDir(), st)
	defer m.Close(ctx)
	if _, err := m.InitializeBed(ctx, "pending"); err != nil {
		t.Fatal(err)
	}
	<-st.started
	_, err := m.InitializeBedWithOptions(ctx, "pending", CreateOptions{Store: "noop"})
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
	st := &countedStore{fakeStore: newFakeStore()}
	m := storeTestManager(t, root, st)
	dir := filepath.Join(root, "orphan")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveMeta(dir, bedMeta{Version: 1, BedID: "orphan", Store: "noop"}); err != nil {
		t.Fatal(err)
	}
	m.SetLuggageLimits(1, 0)
	if got := m.CollectLuggage(ctx); len(got) != 1 {
		t.Fatalf("collected=%v", got)
	}
	if len(st.calls) != 0 {
		t.Fatalf("noop GC touched backend: %v", st.calls)
	}
}

func TestOmittedBedStoreInheritsConfiguredBackend(t *testing.T) {
	ctx := context.Background()
	st := &countedStore{fakeStore: newFakeStore()}
	m := storeTestManager(t, t.TempDir(), st)
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
	m := storeTestManager(t, t.TempDir(), store.Noop{})
	b, err := m.Ensure(ctx, "durable")
	if err != nil {
		t.Fatal(err)
	}
	// Install the selected backend before starting the controller; the unit
	// fixture avoids external S3 while keeping a noop instance default.
	st := newFakeStore()
	b.store = st
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

type failingDeleteStore struct {
	*fakeStore
	fail bool
}

func (s *failingDeleteStore) Delete(ctx context.Context, id string) error {
	if s.fail {
		return errors.New("delete unavailable")
	}
	return s.fakeStore.Delete(ctx, id)
}

func TestPurgeKeepsMetadataUntilSnapshotDeletionSucceeds(t *testing.T) {
	ctx := context.Background()
	st := &failingDeleteStore{fakeStore: newFakeStore(), fail: true}
	m := storeTestManager(t, t.TempDir(), st)
	defer m.Close(ctx)
	b, err := m.Ensure(ctx, "retry")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Purge(ctx, b.ID); err == nil {
		t.Fatal("expected delete failure")
	}
	if meta, ok := loadMeta(b.Dir); !ok || meta.Store != st.Name() {
		t.Fatalf("purge lost retry metadata: %+v", meta)
	}
	st.fail = false
	if err := m.Purge(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("purge retained local data: %v", err)
	}
}
