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

package store

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newTestTar() (*tarStore, *memObj) {
	obj := newMemObj()
	return &tarStore{obj: obj, prefix: "sandbox"}, obj
}

func TestTarObjectLayoutAndRoundtrip(t *testing.T) {
	ctx := context.Background()
	s, obj := newTestTar()
	src := t.TempDir()
	writeTree(t, src)

	if err := s.Persist(ctx, "bed1", src, 1); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if got := s.snapshotKey("bed1"); got != "sandbox/tar/bed1/snapshot.tar.gz" {
		t.Fatalf("snapshot key = %q", got)
	}
	keys := obj.keys(s.bedPrefix("bed1"))
	if len(keys) != 1 || keys[0] != s.snapshotKey("bed1") {
		t.Fatalf("objects = %v, want one tar.gz", keys)
	}
	info, err := s.Stat(ctx, "bed1")
	if err != nil || info == nil || info.Generation != 1 || info.Bytes <= 0 {
		t.Fatalf("stat = %+v, %v", info, err)
	}

	dst := t.TempDir()
	if err := s.Restore(ctx, "bed1", dst); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, name := range []string{"meta.json", "data/workspace/big.bin", "data/workspace/src/b/b.go", "data/workspace/.hidden", "data/workspace/note.local", "data/workspace/tmpfile"} {
		want, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("restored %s differs: %v", name, err)
		}
	}
	for _, name := range []string{"skip.local", "runtime.json"} {
		if _, err := os.Lstat(filepath.Join(dst, name)); !os.IsNotExist(err) {
			t.Fatalf("runtime-local top-level %s leaked into snapshot: %v", name, err)
		}
	}
	for _, name := range []string{"tmp", "memory", "cache"} {
		if _, err := os.Lstat(filepath.Join(dst, "data", name)); !os.IsNotExist(err) {
			t.Fatalf("runtime-local data/%s leaked into snapshot: %v", name, err)
		}
	}
	if target, err := os.Readlink(filepath.Join(dst, "data/workspace/link")); err != nil || target != "src/a.go" {
		t.Fatalf("symlink = %q, %v; want src/a.go", target, err)
	}
	if fi, err := os.Stat(filepath.Join(dst, "data/workspace/exec.sh")); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("exec bit lost: %v, %v", fi, err)
	}
	if fi, err := os.Stat(filepath.Join(dst, "data/workspace/empty")); err != nil || !fi.IsDir() {
		t.Fatalf("empty dir not restored: %v", err)
	}
}

func TestTarAlwaysReplacesFullSnapshot(t *testing.T) {
	ctx := context.Background()
	s, obj := newTestTar()
	src := t.TempDir()
	writeTree(t, src)

	if err := s.Persist(ctx, "bed1", src, 1); err != nil {
		t.Fatal(err)
	}
	before := obj.puts
	if err := s.Persist(ctx, "bed1", src, 2); err != nil {
		t.Fatal(err)
	}
	if delta := obj.puts - before; delta != 1 {
		t.Fatalf("unchanged persist made %d puts, want one full replacement", delta)
	}
	if got := len(obj.keys(s.bedPrefix("bed1"))); got != 1 {
		t.Fatalf("object count = %d, want one", got)
	}

	want := []byte("package a // changed\n")
	if err := os.WriteFile(filepath.Join(src, "data/workspace/src/a.go"), want, 0o644); err != nil {
		t.Fatal(err)
	}
	before = obj.puts
	if err := s.Persist(ctx, "bed1", src, 3); err != nil {
		t.Fatal(err)
	}
	if delta := obj.puts - before; delta != 1 {
		t.Fatalf("changed persist made %d puts, want one full replacement", delta)
	}
	dst := t.TempDir()
	if err := s.Restore(ctx, "bed1", dst); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dst, "data/workspace/src/a.go")); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("restored a.go = %q, %v", got, err)
	}
}

func TestTarPersistsConfiguredBedFSPaths(t *testing.T) {
	filter, err := newSnapshotFilter([]string{"/workspace", "/memory"})
	if err != nil {
		t.Fatal(err)
	}
	obj := newMemObj()
	s := newTarStore(obj, "sandbox", filter)
	src := t.TempDir()
	writeTree(t, src)
	if err := s.Persist(t.Context(), "bed1", src, 1); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := s.Restore(t.Context(), "bed1", dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "data/memory/nested/discard.txt")); err != nil {
		t.Fatalf("configured /memory was not restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "data/cache")); !os.IsNotExist(err) {
		t.Fatalf("unconfigured /cache leaked into snapshot: %v", err)
	}
}

func TestTarConflictAndDelete(t *testing.T) {
	ctx := context.Background()
	s, obj := newTestTar()
	src := t.TempDir()
	writeTree(t, src)

	if err := s.Persist(ctx, "bed1", src, 5); err != nil {
		t.Fatal(err)
	}
	if err := s.Persist(ctx, "bed1", src, 5); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale persist = %v, want ErrConflict", err)
	}
	if err := s.Delete(ctx, "bed1"); err != nil {
		t.Fatal(err)
	}
	if got := len(obj.keys(s.bedPrefix("bed1"))); got != 0 {
		t.Fatalf("%d objects survived delete", got)
	}
	if info, err := s.Stat(ctx, "bed1"); err != nil || info != nil {
		t.Fatalf("stat after delete = %v, %v; want nil, nil", info, err)
	}
}

func TestTarRestoreRejectsPathEscape(t *testing.T) {
	s, obj := newTestTar()
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	content := []byte("escape")
	if err := tw.WriteHeader(&tar.Header{Name: "../outside", Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	obj.data[s.snapshotKey("bed1")] = archive.Bytes()

	dst := t.TempDir()
	if err := s.Restore(context.Background(), "bed1", dst); err == nil {
		t.Fatal("path traversal archive restored successfully")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dst), "outside")); !os.IsNotExist(err) {
		t.Fatalf("archive escaped restore root: %v", err)
	}
}
