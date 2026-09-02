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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/folbricht/desync"
)

// memObj is an in-memory objAPI so the whole cas flow — chunking, transfer
// skipping, commit, GC, restore — runs in unit tests without S3.
type memObj struct {
	mu   sync.Mutex
	data map[string][]byte
	meta map[string]map[string]string
	puts int // total put calls, for asserting incremental behavior
}

func newMemObj() *memObj {
	return &memObj{data: make(map[string][]byte), meta: make(map[string]map[string]string)}
}

func (m *memObj) head(_ context.Context, key string) (map[string]string, int64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[key]
	if !ok {
		return nil, 0, false, nil
	}
	meta := make(map[string]string, len(m.meta[key]))
	for k, v := range m.meta[key] {
		meta[k] = v
	}
	return meta, int64(len(b)), true, nil
}

func (m *memObj) get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[key]
	if !ok {
		return nil, fmt.Errorf("memobj: %s not found", key)
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), b...))), nil
}

func (m *memObj) put(_ context.Context, key string, r io.ReadSeeker, _ int64, meta map[string]string) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = b
	m.meta[key] = meta
	m.puts++
	return nil
}

func (m *memObj) del(_ context.Context, keys []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		delete(m.data, k)
		delete(m.meta, k)
	}
	return nil
}

func (m *memObj) list(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for k := range m.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *memObj) keys(prefix string) []string {
	ks, _ := m.list(context.Background(), prefix)
	return ks
}

// writeTree lays out a bed dir with enough shape to exercise the catar path:
// a multi-chunk file, small files in nested dirs, a symlink, an executable,
// an empty dir, portable meta and a host-private *.local.
func writeTree(t *testing.T, dir string) {
	t.Helper()
	rnd := rand.New(rand.NewSource(42))
	big := make([]byte, 3<<20) // spans several chunks at 64K/256K/1M
	rnd.Read(big)
	files := map[string][]byte{
		"meta.json":                      []byte(`{"generation":1}`),
		"skip.local":                     []byte("host private"),
		"runtime.json":                   []byte("not portable metadata"),
		"data/workspace/big.bin":         big,
		"data/workspace/src/a.go":        []byte("package a\n"),
		"data/workspace/src/b/b.go":      []byte("package b\n"),
		"data/workspace/.hidden":         []byte("dot"),
		"data/workspace/note.local":      []byte("workspace data, must be kept"),
		"data/workspace/exec.sh":         []byte("#!/bin/sh\necho hi\n"),
		"data/workspace/tmpfile":         []byte("ordinary workspace data"),
		"data/tmp/discard.txt":           []byte("runtime-local"),
		"data/memory/nested/discard.txt": []byte("runtime-local projection source"),
		"data/cache/discard.txt":         []byte("another runtime-local root"),
	}
	for p, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(dir, "data/workspace/exec.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "data/workspace/empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("src/a.go", filepath.Join(dir, "data/workspace/link")); err != nil {
		t.Fatal(err)
	}
}

func newTestCAS() (*casStore, *memObj) {
	obj := newMemObj()
	return &casStore{obj: obj, prefix: "sandbox"}, obj
}

func TestCASObjectKeysAreBedScoped(t *testing.T) {
	s, _ := newTestCAS()
	if got := s.indexKey("bed1"); got != "sandbox/bed1/index.caibx" {
		t.Fatalf("index key = %q", got)
	}
	if got := s.chunkPrefix("bed1"); got != "sandbox/bed1/chunks/" {
		t.Fatalf("chunk prefix = %q", got)
	}
}

func TestCASRoundtrip(t *testing.T) {
	ctx := context.Background()
	s, obj := newTestCAS()
	src := t.TempDir()
	writeTree(t, src)

	if err := s.Persist(ctx, "bed1", src, 1); err != nil {
		t.Fatalf("persist: %v", err)
	}
	info, err := s.Stat(ctx, "bed1")
	if err != nil || info == nil {
		t.Fatalf("stat: %v, info=%v", err, info)
	}
	if info.Generation != 1 || info.Bytes <= 0 {
		t.Fatalf("stat = %+v, want generation 1 and bytes > 0", info)
	}

	dst := t.TempDir()
	if err := s.Restore(ctx, "bed1", dst); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, p := range []string{"meta.json", "data/workspace/big.bin", "data/workspace/src/b/b.go", "data/workspace/.hidden", "data/workspace/note.local", "data/workspace/tmpfile"} {
		want, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(p)))
		if err != nil {
			t.Fatalf("restored %s: %v", p, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("restored %s differs (%d vs %d bytes)", p, len(got), len(want))
		}
	}
	for _, name := range []string{"skip.local", "runtime.json"} {
		if _, err := os.Lstat(filepath.Join(dst, name)); !os.IsNotExist(err) {
			t.Fatalf("runtime-local top-level %s leaked into snapshot: err=%v", name, err)
		}
	}
	for _, name := range []string{"tmp", "memory", "cache"} {
		if _, err := os.Lstat(filepath.Join(dst, "data", name)); !os.IsNotExist(err) {
			t.Fatalf("runtime-local data/%s leaked into snapshot: err=%v", name, err)
		}
	}
	if target, err := os.Readlink(filepath.Join(dst, "data/workspace/link")); err != nil || target != "src/a.go" {
		t.Fatalf("symlink = %q, %v; want src/a.go", target, err)
	}
	if fi, err := os.Stat(filepath.Join(dst, "data/workspace/exec.sh")); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("exec bit lost: %v %v", fi.Mode(), err)
	}
	if fi, err := os.Stat(filepath.Join(dst, "data/workspace/empty")); err != nil || !fi.IsDir() {
		t.Fatalf("empty dir not restored: %v", err)
	}

	// memObj sanity: every stored chunk is referenced by the index.
	idx, err := s.loadIndex(ctx, "bed1")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(obj.keys(s.chunkPrefix("bed1"))); got != len(dedupIDs(idx)) {
		t.Fatalf("chunk objects = %d, index references %d unique", got, len(dedupIDs(idx)))
	}
}

func TestCASIncrementalAndGC(t *testing.T) {
	ctx := context.Background()
	s, obj := newTestCAS()
	src := t.TempDir()
	writeTree(t, src)

	if err := s.Persist(ctx, "bed1", src, 1); err != nil {
		t.Fatal(err)
	}
	basePuts := obj.puts

	// Touch one small file: the next persist should move a handful of chunks
	// (the region around the change) plus the index — nowhere near a re-upload.
	if err := os.WriteFile(filepath.Join(src, "data/workspace/src/a.go"), []byte("package a // changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Persist(ctx, "bed1", src, 2); err != nil {
		t.Fatal(err)
	}
	delta := obj.puts - basePuts
	if delta > 6 { // index + a few chunks; full tree is ~15+ chunks
		t.Fatalf("incremental persist uploaded %d objects, expected a small delta", delta)
	}
	if info, _ := s.Stat(ctx, "bed1"); info.Generation != 2 {
		t.Fatalf("generation = %d, want 2", info.Generation)
	}

	// GC: no unreferenced chunks may remain after the second persist.
	idx, err := s.loadIndex(ctx, "bed1")
	if err != nil {
		t.Fatal(err)
	}
	keep := make(map[string]bool)
	for _, c := range idx.Chunks {
		keep[c.ID.String()] = true
	}
	for _, key := range obj.keys(s.chunkPrefix("bed1")) {
		if !keep[filepath.Base(key)] {
			t.Fatalf("orphan chunk survived GC: %s", key)
		}
	}

	// Restore of generation 2 sees the change.
	dst := t.TempDir()
	if err := s.Restore(ctx, "bed1", dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "data/workspace/src/a.go"))
	if err != nil || !strings.Contains(string(got), "changed") {
		t.Fatalf("restored a.go = %q, %v", got, err)
	}
}

func TestCASUnchangedContentUpdatesGeneration(t *testing.T) {
	ctx := context.Background()
	s, obj := newTestCAS()
	src := t.TempDir()
	writeTree(t, src)

	if err := s.Persist(ctx, "bed1", src, 1); err != nil {
		t.Fatal(err)
	}
	before := obj.puts
	beforeIndex, err := s.loadIndex(ctx, "bed1")
	if err != nil {
		t.Fatal(err)
	}
	// Same content, higher generation: chunks stay untouched, but the index
	// is committed once so remote freshness follows the new generation.
	if err := s.Persist(ctx, "bed1", src, 2); err != nil {
		t.Fatal(err)
	}
	if delta := obj.puts - before; delta != 1 {
		t.Fatalf("unchanged persist made %d puts, want index only", delta)
	}
	afterIndex, err := s.loadIndex(ctx, "bed1")
	if err != nil {
		t.Fatal(err)
	}
	if !sameChunks(beforeIndex, afterIndex) {
		t.Fatal("unchanged persist changed content chunks")
	}
	if info, _ := s.Stat(ctx, "bed1"); info.Generation != 2 {
		t.Fatalf("generation = %d, want 2", info.Generation)
	}
}

func TestCASConflict(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestCAS()
	src := t.TempDir()
	writeTree(t, src)

	if err := s.Persist(ctx, "bed1", src, 5); err != nil {
		t.Fatal(err)
	}
	// A writer whose generation isn't ahead of the remote lost the race.
	err := s.Persist(ctx, "bed1", src, 5)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("persist with stale generation: %v, want ErrConflict", err)
	}
}

func TestCASDelete(t *testing.T) {
	ctx := context.Background()
	s, obj := newTestCAS()
	src := t.TempDir()
	writeTree(t, src)

	if err := s.Persist(ctx, "bed1", src, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "bed1"); err != nil {
		t.Fatal(err)
	}
	if n := len(obj.keys("")); n != 0 {
		t.Fatalf("%d objects survived delete", n)
	}
	if info, err := s.Stat(ctx, "bed1"); err != nil || info != nil {
		t.Fatalf("stat after delete = %v, %v; want nil, nil", info, err)
	}
}

func dedupIDs(idx desync.Index) map[string]bool {
	ids := make(map[string]bool)
	for _, c := range idx.Chunks {
		ids[c.ID.String()] = true
	}
	return ids
}
