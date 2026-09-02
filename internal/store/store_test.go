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

import "testing"

func testS3Config() Config {
	return Config{
		Bucket: "b", Region: "us-east-1",
		AccessKeyID: "test-access-key", SecretAccessKey: "test-secret-key",
	}
}

func TestBackendSelection(t *testing.T) {
	st, err := New(t.Context(), Config{Backend: "noop"})
	if err != nil || st.Name() != "noop" {
		t.Fatalf("New noop: %v %v", st, err)
	}
	info, err := st.Stat(t.Context(), "x")
	if err != nil || info != nil {
		t.Fatalf("noop Stat = %v %v", info, err)
	}
	for _, backend := range []string{"s3", "cas" /* alias */, "pack", "tar"} {
		if _, err := New(t.Context(), Config{Backend: backend}); err == nil {
			t.Fatalf("%s without bucket should fail", backend)
		}
	}
	if _, err := New(t.Context(), Config{Backend: "bogus"}); err == nil {
		t.Fatal("unknown backend should fail")
	}
	// auto without persistence config is noop; with a bucket it routes per bed.
	if st, err := New(t.Context(), Config{Backend: "auto"}); err != nil || st.Name() != "noop" {
		t.Fatalf("auto without bucket = %v, %v; want noop", st, err)
	}
	cfg := testS3Config()
	cfg.Backend = "auto"
	if st, err := New(t.Context(), cfg); err != nil || st.Name() != "auto" {
		t.Fatalf("auto with bucket = %v, %v; want auto", st, err)
	}
	cfg.AutoPackFileThreshold = -1
	if _, err := New(t.Context(), cfg); err == nil {
		t.Fatal("auto with negative file threshold should fail")
	}
	cfg = testS3Config()
	cfg.Backend = "pack"
	if st, err := New(t.Context(), cfg); err != nil || st.Name() != "pack" {
		t.Fatalf("pack with bucket = %v, %v; want pack", st, err)
	}
	cfg.Backend = "tar"
	if st, err := New(t.Context(), cfg); err != nil || st.Name() != "tar" {
		t.Fatalf("tar with bucket = %v, %v; want tar", st, err)
	}
}

func TestSnapshotFilterDefaultsToWorkspaceAndSupportsMultiplePaths(t *testing.T) {
	filter, err := newSnapshotFilter(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".", "meta.json", "data", "data/workspace", "data/workspace/a.txt"} {
		if filter.excluded(rel) {
			t.Errorf("default filter excluded %q", rel)
		}
	}
	for _, rel := range []string{"runtime.json", "data/memory", "data/cache/a.txt"} {
		if !filter.excluded(rel) {
			t.Errorf("default filter persisted %q", rel)
		}
	}

	filter, err = newSnapshotFilter([]string{"/workspace", "/memory"})
	if err != nil {
		t.Fatal(err)
	}
	if filter.excluded("data/memory/MEMORY.md") {
		t.Fatal("explicit /memory persist path was excluded")
	}
	if _, err := newSnapshotFilter([]string{"/workspace", "/workspace/cache"}); err == nil {
		t.Fatal("overlapping persist paths were accepted")
	}
}
