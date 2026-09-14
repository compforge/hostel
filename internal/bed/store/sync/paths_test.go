package sync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSharedStoreUsesEachBedsSyncPaths(t *testing.T) {
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
			selections := [][]string{nil, {"/memory"}, {}}
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
				for j, p := range []string{"data/workspace", "data/memory"} {
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
