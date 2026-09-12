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
	"path/filepath"
	"testing"
)

func TestPathProjectionContract(t *testing.T) {
	projection, err := NewPathProjection("/memory", "/mnt/memory")
	if err != nil {
		t.Fatal(err)
	}
	if projection.BedPath != "/memory" || projection.ProcessPath != "/mnt/memory" {
		t.Fatalf("projection = %+v", projection)
	}
	if got := projection.CarrierPath("/beds/b1/data"); got != filepath.FromSlash("/beds/b1/data/memory") {
		t.Fatalf("carrier path = %q", got)
	}

	for _, test := range []struct{ bed, process string }{
		{"memory", "/mnt/memory"},
		{"/memory", "mnt/memory"},
		{"/", "/mnt/root"},
		{"/workspace/cache", "/mnt/cache"},
		{"/cache", "/workspace/cache"},
		{"/devices", "/dev/cache"},
	} {
		if _, err := NewPathProjection(test.bed, test.process); err == nil {
			t.Errorf("NewPathProjection(%q, %q) succeeded", test.bed, test.process)
		}
	}
}

func TestPathProjectionsRejectOverlap(t *testing.T) {
	one, _ := NewPathProjection("/memory", "/mnt/memory")
	two, _ := NewPathProjection("/memory/cache", "/mnt/cache")
	if err := ValidatePathProjections([]PathProjection{one, two}); err == nil {
		t.Fatal("overlapping Bed paths were accepted")
	}
	two, _ = NewPathProjection("/cache", "/mnt/memory/cache")
	if err := ValidatePathProjections([]PathProjection{one, two}); err == nil {
		t.Fatal("overlapping process paths were accepted")
	}
}
