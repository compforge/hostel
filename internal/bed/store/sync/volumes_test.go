package sync

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestSharedStoreUsesEachBedsVolumeRoots(t *testing.T) {
	for _, name := range []string{"auto", "cas", "pack", "tar"} {
		t.Run(name, func(t *testing.T) {
			obj := newMemObj()
			var s Store
			switch name {
			case "auto":
				s = newAutoStore(obj, "test", 1)
			case "cas":
				s = newCASStore(obj, "test")
			case "pack":
				s = newPackStore(obj, "test")
			case "tar":
				s = newTarStore(obj, "test")
			}
			src := t.TempDir()
			writeTree(t, src)
			if err := os.MkdirAll(filepath.Join(src, "volumes", "runtime"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(src, "volumes", "runtime", "token"), []byte("temporary"), 0600); err != nil {
				t.Fatal(err)
			}
			selections := [][]string{nil, {"volumes/memory"}, {}}
			for i, paths := range selections {
				id := []string{"default", "custom", "metadata"}[i]
				if err := s.Persist(t.Context(), id, src, 1, paths); err != nil {
					t.Fatal(err)
				}
			}
			for i := range selections {
				id := []string{"default", "custom", "metadata"}[i]
				dst := t.TempDir()
				if err := s.Restore(t.Context(), id, dst); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(dst, "meta.json")); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(dst, "volumes", "runtime")); !os.IsNotExist(err) {
					t.Fatalf("temporary volume entered snapshot: %v", err)
				}
				for j, p := range []string{"volumes/workspace", "volumes/memory"} {
					_, err := os.Stat(filepath.Join(dst, p))
					want := i == j
					if (err == nil) != want {
						t.Fatalf("%s path %s exists=%t want=%t", id, p, err == nil, want)
					}
				}
			}
		})
	}

}

type failingVolumeObjects struct {
	*memObj
	fail bool
}

func (m *failingVolumeObjects) Put(ctx context.Context, key string, reader io.ReadSeeker, size int64, metadata map[string]string) error {
	if m.fail {
		return errors.New("injected upload failure")
	}
	return m.memObj.Put(ctx, key, reader, size, metadata)
}

func TestFailedMultiVolumeCheckpointKeepsPreviousGeneration(t *testing.T) {
	for _, kind := range []string{"cas", "pack", "tar", "auto"} {
		t.Run(kind, func(t *testing.T) {
			obj := &failingVolumeObjects{memObj: newMemObj()}
			var st Store
			switch kind {
			case "cas":
				st = newCASStore(obj, "test")
			case "pack":
				st = newPackStore(obj, "test")
			case "tar":
				st = newTarStore(obj, "test")
			case "auto":
				st = newAutoStore(obj, "test", 1)
			}
			source := t.TempDir()
			if err := os.WriteFile(filepath.Join(source, "meta.json"), []byte(`{"version":2}`), 0600); err != nil {
				t.Fatal(err)
			}
			roots := []string{"volumes/first", "volumes/second"}
			for _, root := range roots {
				if err := os.MkdirAll(filepath.Join(source, root), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(source, root, "value"), []byte("committed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.Persist(t.Context(), "bed", source, 1, roots); err != nil {
				t.Fatal(err)
			}
			for _, root := range roots {
				if err := os.WriteFile(filepath.Join(source, root, "value"), []byte("uncommitted"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			obj.fail = true
			if err := st.Persist(t.Context(), "bed", source, 2, roots); err == nil {
				t.Fatal("failed upload accepted")
			}
			snapshot, err := st.Stat(t.Context(), "bed")
			if err != nil || snapshot == nil || snapshot.Generation != 1 {
				t.Fatalf("generation advanced: %+v %v", snapshot, err)
			}
			destination := t.TempDir()
			if err := st.Restore(t.Context(), "bed", destination); err != nil {
				t.Fatal(err)
			}
			for _, root := range roots {
				got, err := os.ReadFile(filepath.Join(destination, root, "value"))
				if err != nil || string(got) != "committed" {
					t.Fatalf("partial checkpoint visible: %q %v", got, err)
				}
			}
		})
	}
}
