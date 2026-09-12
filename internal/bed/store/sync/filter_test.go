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

package sync

import "testing"

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
