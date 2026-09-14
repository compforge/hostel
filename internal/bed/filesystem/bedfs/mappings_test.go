package bedfs

import (
	"context"
	model "github.com/qiankunli/hostel/internal/bed"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mappedFixture(t *testing.T, readOnly bool) (*FS, string) {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "beds", "one", "data")
	external := filepath.Join(base, "volume")
	for _, p := range []string{filepath.Join(home, "workspace"), external} {
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(external, "input.txt"), []byte("input"), 0644); err != nil {
		t.Fatal(err)
	}
	fs, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fs.Close() })
	if err := fs.SetPathMappings([]model.PathMapping{{HostPath: external, BedPath: "/mnt/project", ReadOnly: readOnly}}); err != nil {
		t.Fatal(err)
	}
	return fs, external
}
func TestPathMappingsDefaultRootAndExternalData(t *testing.T) {
	fs, external := mappedFixture(t, false)
	if err := fs.Write("/tmp/private", []byte("private"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fs.Home(), "tmp/private")); err != nil {
		t.Fatal(err)
	}
	if got, err := fs.Read("/mnt/project/input.txt"); err != nil || string(got) != "input" {
		t.Fatalf("read=%q,%v", got, err)
	}
	if err := fs.Write("/mnt/project/new.txt", []byte("new"), 0); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(external, "new.txt")); err != nil || string(got) != "new" {
		t.Fatalf("write=%q,%v", got, err)
	}
	if err := fs.Rename("/mnt/project/new.txt", "/mnt/project/moved.txt"); err != nil {
		t.Fatal(err)
	}
	host, err := fs.Resolve("/mnt/project/moved.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := WorkspaceView(fs).Path(host); err != nil || got != "/mnt/project/moved.txt" {
		t.Fatalf("view=%q,%v", got, err)
	}
	entries, err := fs.List("/", 4)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		found = found || e.Path == "/mnt/project/input.txt"
	}
	if !found {
		t.Fatalf("list omitted mapping: %+v", entries)
	}
	matches, err := fs.Search("/", "input.txt")
	if err != nil || len(matches) != 1 || matches[0].Path != "/mnt/project/input.txt" {
		t.Fatalf("search=%+v,%v", matches, err)
	}
	if err := fs.Rename("/mnt/project/moved.txt", "/workspace/moved.txt"); err == nil {
		t.Fatal("cross-root rename accepted")
	}
	for _, p := range []string{"/", "/mnt", "/mnt/project"} {
		if err := fs.RemoveDir(p); err == nil {
			t.Fatalf("removed mapping ancestor %s", p)
		}
	}
	if err := fs.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(fs.Home())); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(external, "input.txt")); err != nil || string(got) != "input" {
		t.Fatalf("external data removed: %v", err)
	}
}
func TestReadOnlyMappingAndTransfer(t *testing.T) {
	fs, external := mappedFixture(t, true)
	mutations := []func() error{
		func() error { return fs.Write("/mnt/project/x", nil, 0) },
		func() error { return fs.Remove([]string{"/mnt/project/input.txt"}) },
		func() error { return fs.Rename("/mnt/project/input.txt", "/mnt/project/x") },
		func() error { return fs.Chmod("/mnt/project/input.txt", Permission{Mode: 0600}) },
		func() error {
			_, e := fs.Replace("/mnt/project/input.txt", ReplaceItem{Old: "input", New: "changed"})
			return e
		},
		func() error { return fs.MakeDir("/mnt/project/sub") },
		func() error {
			_, e := fs.WriteTransferFile(context.Background(), "/mnt/project/input.txt", strings.NewReader("changed"), true)
			return e
		},
		func() error {
			return fs.ImportTransferTree(context.Background(), t.TempDir(), "/mnt/project", true, nil)
		},
	}
	for i, mutate := range mutations {
		if err := mutate(); !os.IsPermission(err) {
			t.Fatalf("mutation %d: %v", i, err)
		}
	}
	cwd, err := fs.Resolve("/mnt/project")
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.EnsureDir(cwd); err != nil {
		t.Fatal(err)
	}
	if err := fs.EnsureDir(filepath.Join(cwd, "missing")); err == nil {
		t.Fatal("created read-only cwd")
	}
	if err := os.Symlink(fs.Home(), filepath.Join(external, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Read("/mnt/project/escape/workspace/file"); err == nil {
		t.Fatal("escaped mapped root")
	}
	fallback := t.TempDir()
	if err := os.MkdirAll(filepath.Join(fallback, "mnt/project"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fallback, "mnt/project/missing"), []byte("leak"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewReader(fs, fallback).Read("/mnt/project/missing"); !os.IsNotExist(err) {
		t.Fatalf("mapping fell back: %v", err)
	}
}
