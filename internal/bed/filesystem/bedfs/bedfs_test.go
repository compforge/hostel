// Copyright 2026 Li Qiankun
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bedfs

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestFS(t *testing.T, root string) *FS {
	t.Helper()
	filesystem, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = filesystem.Close() })
	return filesystem
}

func TestResolveConfinement(t *testing.T) {
	root := t.TempDir()
	o := newTestFS(t, root)

	cases := []struct {
		in      string
		wantErr bool
		wantAbs string // expected host path (when no error)
	}{
		{"a.txt", false, filepath.Join(root, "workspace", "a.txt")}, // relative = workspace-relative
		{"/workspace/a.txt", false, filepath.Join(root, "workspace", "a.txt")},
		{"/workspace", false, filepath.Join(root, "workspace")}, // a real subdir, not an alias for the root
		{"/workspace/sub/b", false, filepath.Join(root, "workspace", "sub", "b")},
		{"/", false, root}, // the client's "/" is bed_home
		{"/workspace/../../etc/passwd", false, filepath.Join(root, "etc", "passwd")}, // .. neutralized under bed_home
		{"../escape", false, filepath.Join(root, "escape")},                          // climbs out of the workspace but never out of the bed
		{"/etc/passwd", false, filepath.Join(root, "etc", "passwd")},                 // absolute paths are bed-rooted
		{"~/secrets", true, ""},
		{"", true, ""},
	}
	for _, tc := range cases {
		got, err := o.Resolve(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("Resolve(%q): want error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Resolve(%q): unexpected error %v", tc.in, err)
			continue
		}
		if got != tc.wantAbs {
			t.Errorf("Resolve(%q) = %q, want %q", tc.in, got, tc.wantAbs)
		}
	}
}

func TestWriteReadListReplace(t *testing.T) {
	o := newTestFS(t, t.TempDir())

	if err := o.Write("/workspace/dir/hello.txt", []byte("localhost:8080\nkeep\n"), 0); err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, err := o.Read("/workspace/dir/hello.txt")
	if err != nil || string(data) != "localhost:8080\nkeep\n" {
		t.Fatalf("Read: %q err=%v", data, err)
	}

	// Stat reports the virtual path back.
	fi, err := o.Stat("dir/hello.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Path != "/workspace/dir/hello.txt" || fi.Type != "file" {
		t.Fatalf("Stat path=%q type=%q", fi.Path, fi.Type)
	}

	// List depth 1 shows the dir; depth 2 shows the file.
	shallow, err := o.List("/workspace", 1)
	if err != nil || len(shallow) != 1 || shallow[0].Type != "directory" {
		t.Fatalf("List depth1: %+v err=%v", shallow, err)
	}
	deep, err := o.List("/workspace", 2)
	if err != nil || len(deep) != 2 {
		t.Fatalf("List depth2: want 2 entries, got %+v err=%v", deep, err)
	}

	// Replace counts + rewrites.
	res, err := o.Replace("dir/hello.txt", ReplaceItem{Old: "localhost:8080", New: "0.0.0.0:9090"})
	if err != nil || res.ReplacedCount != 1 {
		t.Fatalf("Replace: %+v err=%v", res, err)
	}
	data, _ = o.Read("dir/hello.txt")
	if string(data) != "0.0.0.0:9090\nkeep\n" {
		t.Fatalf("after Replace: %q", data)
	}
}

// BedFS is a daemon-side security boundary: a symlink created by bed code must
// not make file APIs resolve a carrier path outside bed_home. Internal relative
// symlinks remain valid because they do not cross that boundary.
func TestFileOperationsConfineSymlinkTargets(t *testing.T) {
	root := t.TempDir()
	o := newTestFS(t, root)
	if err := o.MakeDir("/workspace"); err != nil {
		t.Fatal(err)
	}

	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "workspace", "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Read("/workspace/escape/secret.txt"); err == nil {
		t.Fatal("Read followed a symlink outside bed_home")
	}
	if err := o.Write("/workspace/escape/secret.txt", []byte("overwritten"), 0); err == nil {
		t.Fatal("Write followed a symlink outside bed_home")
	}
	if _, err := o.Stat("/workspace/escape/secret.txt"); err == nil {
		t.Fatal("Stat followed a symlink outside bed_home")
	}
	if _, err := o.Replace("/workspace/escape/secret.txt", ReplaceItem{Old: "outside", New: "changed"}); err == nil {
		t.Fatal("Replace followed a symlink outside bed_home")
	}
	if err := o.Chmod("/workspace/escape/secret.txt", Permission{Mode: 0o600}); err == nil {
		t.Fatal("Chmod followed a symlink outside bed_home")
	}
	if err := o.Remove([]string{"/workspace/escape/secret.txt"}); err == nil {
		t.Fatal("Remove followed a symlink outside bed_home")
	}
	if _, err := o.List("/workspace/escape", 1); err == nil {
		t.Fatal("List followed a symlink outside bed_home")
	}
	if _, err := o.Search("/workspace/escape", "*.txt"); err == nil {
		t.Fatal("Search followed a symlink outside bed_home")
	}
	if err := o.MakeDir("/workspace/escape/new"); err == nil {
		t.Fatal("MakeDir followed a symlink outside bed_home")
	}
	if err := o.Write("/workspace/move.txt", []byte("move"), 0); err != nil {
		t.Fatal(err)
	}
	if err := o.Rename("/workspace/move.txt", "/workspace/escape/moved.txt"); err == nil {
		t.Fatal("Rename followed a symlink outside bed_home")
	}
	if got, err := os.ReadFile(secret); err != nil || string(got) != "outside" {
		t.Fatalf("outside file changed: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); !os.IsNotExist(err) {
		t.Fatalf("outside directory created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "moved.txt")); !os.IsNotExist(err) {
		t.Fatalf("outside rename created a file: %v", err)
	}

	if err := o.MakeDir("/workspace/inside"); err != nil {
		t.Fatal(err)
	}
	if err := o.Write("/workspace/inside/value.txt", []byte("inside"), 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inside", filepath.Join(root, "workspace", "alias")); err != nil {
		t.Fatal(err)
	}
	if got, err := o.Read("/workspace/alias/value.txt"); err != nil || string(got) != "inside" {
		t.Fatalf("safe internal symlink = %q, %v", got, err)
	}
}

func TestReadLines(t *testing.T) {
	o := newTestFS(t, t.TempDir())
	_ = o.Write("f", []byte("l0\nl1\nl2\nl3\n"), 0)
	got, err := o.ReadLines("f", 1, 2)
	if err != nil || got != "l1\nl2\n" {
		t.Fatalf("ReadLines(1,2) = %q err=%v", got, err)
	}
	got, _ = o.ReadLines("f", 3, 0) // limit 0 = to end
	if got != "l3\n" {
		t.Fatalf("ReadLines(3,0) = %q", got)
	}
}

func TestSearch(t *testing.T) {
	o := newTestFS(t, t.TempDir())
	_ = o.Write("a/x.go", nil, 0)
	_ = o.Write("a/y.txt", nil, 0)
	_ = o.Write("b/z.go", nil, 0)
	hits, err := o.Search("/workspace", "*.go")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("Search *.go: want 2, got %d (%+v)", len(hits), hits)
	}
}

// Owner inheritance: without root the real cross-uid chown can't run (that's
// exercised by the uid mechanism's boot smoke on real hosts), so these tests
// pin the plumbing around it.
func TestOwnerInheritance(t *testing.T) {
	t.Run("self-owned workspace disables chown", func(t *testing.T) {
		o := newTestFS(t, t.TempDir())
		if o.uid != -1 || o.gid != -1 {
			t.Fatalf("self-owned root: owner = %d:%d, want -1:-1 (no chown)", o.uid, o.gid)
		}
	})

	t.Run("owned paths still created", func(t *testing.T) {
		// Chown-to-self is always permitted, so wiring uid/gid to the current
		// user drives the chownNew/mkdirAllOwned code paths for real.
		root := t.TempDir()
		o := newTestFS(t, root)
		o.uid, o.gid = os.Geteuid(), os.Getegid()

		if err := o.Write("/workspace/a/b/c.txt", []byte("x"), 0); err != nil {
			t.Fatalf("Write nested: %v", err)
		}
		if err := o.MakeDir("d/e"); err != nil {
			t.Fatalf("MakeDir: %v", err)
		}
		if err := o.Rename("a/b/c.txt", "f/moved.txt"); err != nil {
			t.Fatalf("Rename into new dir: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "workspace", "f", "moved.txt")); err != nil {
			t.Fatalf("moved file missing: %v", err)
		}
	})

	t.Run("hardlinked file is not rehomed", func(t *testing.T) {
		root := t.TempDir()
		o := newTestFS(t, root)
		o.uid, o.gid = os.Geteuid(), os.Getegid()

		orig := filepath.Join(root, "orig")
		if err := os.WriteFile(orig, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		linked := filepath.Join(root, "linked")
		if err := os.Link(orig, linked); err != nil {
			t.Fatal(err)
		}
		// Must silently skip (nlink>1), not error or chown the shared inode.
		o.chownNew(linked)
	})
}

func TestEnsureDirCreatesNested(t *testing.T) {
	root := t.TempDir()
	o := newTestFS(t, root)

	// A caller-named workdir deep under the workspace that doesn't exist yet.
	host, err := o.Resolve("/workspace/sub/deep/dir")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := o.EnsureDir(host); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	if fi, err := os.Stat(host); err != nil || !fi.IsDir() {
		t.Fatalf("dir not created (err=%v)", err)
	}
	// Idempotent: a second call on an existing dir is fine.
	if err := o.EnsureDir(host); err != nil {
		t.Fatalf("EnsureDir (repeat): %v", err)
	}
}
