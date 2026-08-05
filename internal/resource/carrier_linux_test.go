//go:build linux

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

package resource

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCgroupCarrierSnapshot(t *testing.T) {
	mount := t.TempDir()
	self := filepath.Join(t.TempDir(), "cgroup")
	if err := os.WriteFile(self, []byte("0::/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"cpu.stat":       "usage_usec 2500000\nuser_usec 2000000\nsystem_usec 500000\n",
		"cpu.max":        "50000 100000\n",
		"memory.current": "750\n",
		"memory.max":     "1000\n",
	}
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(mount, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	carrier, err := newCgroupCarrier(mount, self)
	if err != nil {
		t.Fatalf("newCgroupCarrier: %v", err)
	}
	snapshot, err := carrier.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.CPUUsage != 2500*time.Millisecond || snapshot.CPULimitCores != 0.5 ||
		snapshot.MemoryCurrentBytes != 750 || snapshot.MemoryLimitBytes != 1000 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestCarrierRootEscapesHostelSystemChild(t *testing.T) {
	mount := t.TempDir()
	self := filepath.Join(t.TempDir(), "cgroup")
	if err := os.WriteFile(self, []byte("0::/hostel-system\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := carrierRoot(mount, self)
	if err != nil || root != mount {
		t.Fatalf("carrierRoot = %q, %v; want %q", root, err, mount)
	}
}

func TestUnlimitedCarrierLimits(t *testing.T) {
	if got, err := cpuLimitCores("max 100000"); err != nil || got != 0 {
		t.Fatalf("cpuLimitCores(max) = %v, %v", got, err)
	}
	if got, err := memoryLimit("max"); err != nil || got != 0 {
		t.Fatalf("memoryLimit(max) = %d, %v", got, err)
	}
}
