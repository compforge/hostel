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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAutoTree(t *testing.T, dir string, files int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"generation":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range files {
		name := filepath.Join(dir, "data", "files", string(rune('a'+i))+".txt")
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte{byte('a' + i)}, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAutoDetectsExistingLayout(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name string
		new  func(objAPI) Store
	}{
		{name: "cas", new: func(obj objAPI) Store { return newCASStore(obj, "sandbox") }},
		{name: "pack", new: func(obj objAPI) Store { return newPackStore(obj, "sandbox") }},
		{name: "tar", new: func(obj objAPI) Store { return newTarStore(obj, "sandbox") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			obj := newMemObj()
			src := t.TempDir()
			writeAutoTree(t, src, 2)
			if err := test.new(obj).Persist(ctx, "bed1", src, 3); err != nil {
				t.Fatal(err)
			}
			auto := newAutoStore(obj, "sandbox", 1)
			if info, err := auto.Stat(ctx, "bed1"); err != nil || info == nil || info.Generation != 3 {
				t.Fatalf("auto stat = %+v, %v", info, err)
			}
			dst := t.TempDir()
			if err := auto.Restore(ctx, "bed1", dst); err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join(src, "data/files/a.txt"))
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(dst, "data/files/a.txt"))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("restored file = %q, %v; want %q", got, err, want)
			}
		})
	}
}

func TestAutoStartsPack(t *testing.T) {
	ctx := context.Background()
	obj := newMemObj()
	// Disabling CAS migration must not change the new-bed default.
	auto := newAutoStore(obj, "sandbox", 0)
	src := t.TempDir()
	writeAutoTree(t, src, 2)

	if err := auto.Persist(ctx, "bed1", src, 1); err != nil {
		t.Fatal(err)
	}
	if info, _ := auto.pack.Stat(ctx, "bed1"); info == nil || info.Generation != 1 {
		t.Fatalf("pack snapshot = %+v, want generation 1", info)
	}
	if info, _ := auto.cas.Stat(ctx, "bed1"); info != nil {
		t.Fatalf("unexpected CAS snapshot %+v", info)
	}
}

func TestAutoKeepsExistingCASBelowThreshold(t *testing.T) {
	ctx := context.Background()
	obj := newMemObj()
	auto := newAutoStore(obj, "sandbox", 10)
	src := t.TempDir()
	writeAutoTree(t, src, 2)

	if err := auto.cas.Persist(ctx, "bed1", src, 1); err != nil {
		t.Fatal(err)
	}
	if err := auto.Persist(ctx, "bed1", src, 2); err != nil {
		t.Fatal(err)
	}
	if info, _ := auto.cas.Stat(ctx, "bed1"); info == nil || info.Generation != 2 {
		t.Fatalf("CAS snapshot = %+v, want generation 2", info)
	}
	if info, _ := auto.pack.Stat(ctx, "bed1"); info != nil {
		t.Fatalf("existing CAS unexpectedly switched to pack: %+v", info)
	}
}

func TestAutoSwitchesCASToPackAboveThreshold(t *testing.T) {
	ctx := context.Background()
	obj := newMemObj()
	auto := newAutoStore(obj, "sandbox", 2)
	src := t.TempDir()
	writeAutoTree(t, src, 4)

	if err := auto.cas.Persist(ctx, "bed1", src, 1); err != nil {
		t.Fatal(err)
	}
	if err := auto.Persist(ctx, "bed1", src, 2); err != nil {
		t.Fatal(err)
	}
	if info, _ := auto.pack.Stat(ctx, "bed1"); info == nil || info.Generation != 2 {
		t.Fatalf("pack snapshot = %+v, want generation 2", info)
	}
	// Automatic conversion never deletes durable data. The older CAS layout
	// remains until explicit purge, while auto selects the newer pack snapshot.
	if info, _ := auto.cas.Stat(ctx, "bed1"); info == nil || info.Generation != 1 {
		t.Fatalf("CAS snapshot = %+v, want retained generation 1", info)
	}
	if info, err := auto.Stat(ctx, "bed1"); err != nil || info.Generation != 2 {
		t.Fatalf("auto stat = %+v, %v; want pack generation 2", info, err)
	}

	// Once pack is current, falling below the threshold never downshifts.
	if err := os.Remove(filepath.Join(src, "data/files/d.txt")); err != nil {
		t.Fatal(err)
	}
	if err := auto.Persist(ctx, "bed1", src, 3); err != nil {
		t.Fatal(err)
	}
	if info, _ := auto.pack.Stat(ctx, "bed1"); info == nil || info.Generation != 3 {
		t.Fatalf("pack snapshot after persist = %+v, want generation 3", info)
	}
}

func TestAutoKeepsExistingTar(t *testing.T) {
	ctx := context.Background()
	obj := newMemObj()
	auto := newAutoStore(obj, "sandbox", 1)
	src := t.TempDir()
	writeAutoTree(t, src, 3)

	if err := auto.tar.Persist(ctx, "bed1", src, 1); err != nil {
		t.Fatal(err)
	}
	if err := auto.Persist(ctx, "bed1", src, 2); err != nil {
		t.Fatal(err)
	}
	if info, _ := auto.tar.Stat(ctx, "bed1"); info == nil || info.Generation != 2 {
		t.Fatalf("tar snapshot = %+v, want generation 2", info)
	}
	if info, _ := auto.pack.Stat(ctx, "bed1"); info != nil {
		t.Fatalf("existing tar unexpectedly switched to pack: %+v", info)
	}
}

func TestAutoRejectsAmbiguousLayoutsAndPurgeDeletesAll(t *testing.T) {
	ctx := context.Background()
	obj := newMemObj()
	auto := newAutoStore(obj, "sandbox", 10)
	src := t.TempDir()
	writeAutoTree(t, src, 1)

	if err := auto.cas.Persist(ctx, "bed1", src, 1); err != nil {
		t.Fatal(err)
	}
	if err := auto.pack.Persist(ctx, "bed1", src, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := auto.Stat(ctx, "bed1"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous auto stat = %v", err)
	}
	if err := auto.Delete(ctx, "bed1"); err != nil {
		t.Fatal(err)
	}
	if got := len(obj.keys("")); got != 0 {
		t.Fatalf("%d objects survived auto purge", got)
	}
}

func TestSnapshotFileThresholdExcludesEphemeralPaths(t *testing.T) {
	dir := t.TempDir()
	writeAutoTree(t, dir, 0) // meta.json is the only persistable file.
	for _, name := range []string{"skip.local", "data/tmp/a", "data/tmp/nested/b"} {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("ignored"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if exceeded, err := exceedsSnapshotFileThreshold(dir, 1); err != nil || exceeded {
		t.Fatalf("excluded files exceeded threshold: %v, %v", exceeded, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data", "kept"), []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	if exceeded, err := exceedsSnapshotFileThreshold(dir, 1); err != nil || !exceeded {
		t.Fatalf("persistable file did not exceed threshold: %v, %v", exceeded, err)
	}
}
