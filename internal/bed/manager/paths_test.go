package manager

import (
	"context"
	"errors"
	model "github.com/qiankunli/hostel/internal/bed"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestBedSyncPathsSurviveRestartAndSnapshotRestore(t *testing.T) {
	ctx := context.Background()
	for _, paths := range [][]string{nil, {}, {"/memory", "/workspace"}} {
		t.Run(casePathName(paths), func(t *testing.T) {
			root := t.TempDir()
			st := newFakeStore()
			m := storeTestManager(t, root, st)
			if _, err := m.InitializeBedWithOptions(ctx, "paths", CreateOptions{SyncPaths: paths}); err != nil {
				t.Fatal(err)
			}
			b, err := m.Ensure(ctx, "paths")
			if err != nil {
				t.Fatal(err)
			}
			expected := paths
			if expected == nil {
				expected = []string{"/workspace"}
			}
			if b.Spec().SyncPaths == nil || !slices.Equal(b.Spec().SyncPaths, expected) {
				t.Fatalf("spec paths=%v", b.Spec().SyncPaths)
			}
			if err := m.Checkpoint(ctx, "paths"); err != nil {
				t.Fatal(err)
			}
			if _, err := m.InitializeBedWithOptions(ctx, "paths", CreateOptions{SyncPaths: []string{"/other"}}); !errors.Is(err, ErrPathsConflict) {
				t.Fatalf("conflict=%v", err)
			}
			if err := m.Close(ctx); err != nil {
				t.Fatal(err)
			}
			for _, nextRoot := range []string{root, t.TempDir()} {
				next := storeTestManager(t, nextRoot, st)
				restored, err := next.Ensure(ctx, "paths")
				if err != nil {
					t.Fatal(err)
				}
				if restored.Spec().SyncPaths == nil || !slices.Equal(restored.Spec().SyncPaths, expected) {
					t.Fatalf("restored paths=%v want=%v", restored.Spec().SyncPaths, expected)
				}
				if err := next.Close(ctx); err != nil {
					t.Fatal(err)
				}
			}
			// A fresh carrier cannot overwrite the snapshot's declared data contract.
			conflicting := storeTestManager(t, t.TempDir(), st)
			defer conflicting.Close(ctx)
			if _, err := conflicting.InitializeBedWithOptions(ctx, "paths", CreateOptions{SyncPaths: []string{"/other"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := conflicting.Ensure(ctx, "paths"); !errors.Is(err, ErrPathsConflict) {
				t.Fatalf("snapshot conflict=%v", err)
			}
		})
	}
}
func casePathName(paths []string) string {
	if paths == nil {
		return "default"
	}
	if len(paths) == 0 {
		return "metadata"
	}
	return "custom"
}

func TestLocalIdentityKeepsMappingsOutOfPortableMetadata(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	m := storeTestManager(t, root, newFakeStore())
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "paths")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	local := m.localIdentityLocked("paths")
	m.mu.Unlock()
	expected := []model.PathMapping{{HostPath: "/carrier/volume", BedPath: "/project", ReadOnly: true}}
	model.SpecWriter{}.Update(local.bed, func(s *model.Spec) { s.PathMappings = expected; s.SyncPaths = []string{} })
	if err := m.saveLocalIdentity(local); err != nil {
		t.Fatal(err)
	}
	if err := saveMeta(home, bedMeta{Version: 1, BedID: "paths", SyncPaths: []string{}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	next := storeTestManager(t, root, newFakeStore())
	defer next.Close(ctx)
	if err := next.Start(ctx); err != nil {
		t.Fatal(err)
	}
	actual := next.localIdentities["paths"].bed.Spec()
	if !slices.Equal(actual.PathMappings, expected) || actual.SyncPaths == nil {
		t.Fatalf("local declaration=%+v", actual)
	}
	meta, ok := loadMeta(home)
	if !ok || meta.SyncPaths == nil {
		t.Fatalf("meta=%+v", meta)
	}
}
