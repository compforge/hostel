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

func TestStoreSyncSelection(t *testing.T) {
	st, err := NewManager(t.Context(), Config{Sync: "noop"})
	if err != nil || st.DefaultSync() != "noop" {
		t.Fatalf("New noop: %v %v", st, err)
	}
	info, err := st.Stat(t.Context(), SyncNoop, "x")
	if err != nil || info != nil {
		t.Fatalf("noop Stat = %v %v", info, err)
	}
	for _, backend := range []string{"cas", "pack", "tar"} {
		if st, err := NewManager(t.Context(), Config{Sync: backend}); err != nil || st.DefaultSync() != SyncNoop {
			t.Fatalf("%s without bucket = %v, %v; want noop", backend, st, err)
		}
	}
	if _, err := NewManager(t.Context(), Config{Sync: "bogus"}); err == nil {
		t.Fatal("unknown backend should fail")
	}
	// auto without persistence config is noop; with a bucket it routes per bed.
	if st, err := NewManager(t.Context(), Config{Sync: "auto"}); err != nil || st.DefaultSync() != "noop" {
		t.Fatalf("auto without bucket = %v, %v; want noop", st, err)
	}
	cfg := testS3Config()
	cfg.Sync = "auto"
	if st, err := NewManager(t.Context(), cfg); err != nil || st.DefaultSync() != "auto" {
		t.Fatalf("auto with bucket = %v, %v; want auto", st, err)
	}
	cfg.AutoPackFileThreshold = -1
	if _, err := NewManager(t.Context(), cfg); err == nil {
		t.Fatal("auto with negative file threshold should fail")
	}
	cfg = testS3Config()
	cfg.Sync = "pack"
	if st, err := NewManager(t.Context(), cfg); err != nil || st.DefaultSync() != "pack" {
		t.Fatalf("pack with bucket = %v, %v; want pack", st, err)
	}
	cfg.Sync = "tar"
	if st, err := NewManager(t.Context(), cfg); err != nil || st.DefaultSync() != "tar" {
		t.Fatalf("tar with bucket = %v, %v; want tar", st, err)
	}
}
