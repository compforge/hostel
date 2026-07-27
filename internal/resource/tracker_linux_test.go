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

import "testing"

func TestUnifiedCgroupPath(t *testing.T) {
	path, err := unifiedCgroupPath("0::/kubepods/pod/container\n")
	if err != nil || path != "/kubepods/pod/container" {
		t.Fatalf("unifiedCgroupPath = %q, %v", path, err)
	}
}

func TestCPUUsageMicros(t *testing.T) {
	got, err := cpuUsageMicros("usage_usec 12345\nuser_usec 10000\nsystem_usec 2345\n")
	if err != nil || got != 12345 {
		t.Fatalf("cpuUsageMicros = %d, %v", got, err)
	}
}
