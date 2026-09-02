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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestPack(targetBytes int) (*packStore, *memObj) {
	obj := newMemObj()
	return &packStore{obj: obj, prefix: "sandbox", targetBytes: targetBytes}, obj
}

func TestPackObjectLayoutAndRoundtrip(t *testing.T) {
	ctx := context.Background()
	s, obj := newTestPack(1 << 20)
	src := t.TempDir()
	writeTree(t, src)

	if err := s.Persist(ctx, "bed1", src, 1); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if got := s.headKey("bed1"); got != "sandbox/beds/bed1/head.json" {
		t.Fatalf("head key = %q", got)
	}
	var manifests, packs int
	for _, key := range obj.keys(s.bedPrefix("bed1")) {
		if strings.Contains(key, "/v2/") || strings.Contains(key, "/chunks/") {
			t.Fatalf("unexpected layout key %q", key)
		}
		switch {
		case strings.Contains(key, "/snapshots/") && strings.HasSuffix(key, ".json"):
			manifests++
		case strings.Contains(key, "/packs/") && strings.HasSuffix(key, ".pack"):
			packs++
		}
	}
	if manifests != 1 || packs < 2 {
		t.Fatalf("objects: manifests=%d packs=%d, want one manifest and multiple bounded packs", manifests, packs)
	}

	info, err := s.Stat(ctx, "bed1")
	if err != nil || info == nil || info.Generation != 1 || info.Bytes <= 0 {
		t.Fatalf("stat = %+v, %v", info, err)
	}
	dst := t.TempDir()
	if err := s.Restore(ctx, "bed1", dst); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, name := range []string{"meta.json", "data/workspace/big.bin", "data/workspace/src/b/b.go", "data/workspace/note.local"} {
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
}

func TestPackIncrementalAndUnchanged(t *testing.T) {
	ctx := context.Background()
	s, obj := newTestPack(packTargetBytes)
	src := t.TempDir()
	writeTree(t, src)

	if err := s.Persist(ctx, "bed1", src, 1); err != nil {
		t.Fatal(err)
	}
	beforeChange := obj.puts
	if err := os.WriteFile(filepath.Join(src, "data/workspace/src/a.go"), []byte("package a // changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Persist(ctx, "bed1", src, 2); err != nil {
		t.Fatal(err)
	}
	// A small edit creates one pack plus a manifest and atomically replaces
	// head; unchanged chunks stay referenced in their previous packs.
	if delta := obj.puts - beforeChange; delta != 3 {
		t.Fatalf("incremental persist made %d puts, want pack + manifest + head", delta)
	}

	beforeUnchanged := obj.puts
	if err := s.Persist(ctx, "bed1", src, 3); err != nil {
		t.Fatal(err)
	}
	if delta := obj.puts - beforeUnchanged; delta != 1 {
		t.Fatalf("unchanged persist made %d puts, want head only", delta)
	}
	if info, _ := s.Stat(ctx, "bed1"); info.Generation != 3 {
		t.Fatalf("generation = %d, want 3", info.Generation)
	}
}

func TestPackConflictAndDelete(t *testing.T) {
	ctx := context.Background()
	s, obj := newTestPack(packTargetBytes)
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
		t.Fatalf("%d pack objects survived delete", got)
	}
	if info, err := s.Stat(ctx, "bed1"); err != nil || info != nil {
		t.Fatalf("stat after delete = %v, %v; want nil, nil", info, err)
	}
}
