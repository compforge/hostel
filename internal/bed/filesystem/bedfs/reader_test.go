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
	"os"
	"path/filepath"
	"testing"
)

func TestReaderAbsoluteFallbackAndPrimaryPrecedence(t *testing.T) {
	bedHome := t.TempDir()
	if err := os.Mkdir(filepath.Join(bedHome, "workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	primary, err := New(bedHome)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = primary.Close() })

	carrierDir := t.TempDir()
	carrierFile := filepath.Join(carrierDir, "artifact.txt")
	if err := os.WriteFile(carrierFile, []byte("carrier\nsecond\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reader := NewReader(primary, string(os.PathSeparator))

	data, err := reader.Read(carrierFile)
	if err != nil || string(data) != "carrier\nsecond\n" {
		t.Fatalf("fallback Read = %q, %v", data, err)
	}
	line, err := reader.ReadLines(carrierFile, 1, 1)
	if err != nil || line != "second\n" {
		t.Fatalf("fallback ReadLines = %q, %v", line, err)
	}
	info, err := reader.Stat(carrierFile)
	if err != nil || info.Path != filepath.ToSlash(carrierFile) {
		t.Fatalf("fallback Stat = %+v, %v", info, err)
	}
	listed, err := reader.List(carrierDir, 1)
	if err != nil || len(listed) != 1 || listed[0].Path != filepath.ToSlash(carrierFile) {
		t.Fatalf("fallback List = %+v, %v", listed, err)
	}
	found, err := reader.Search(carrierDir, "artifact.txt")
	if err != nil || len(found) != 1 || found[0].Path != filepath.ToSlash(carrierFile) {
		t.Fatalf("fallback Search = %+v, %v", found, err)
	}

	if err := primary.Write(filepath.ToSlash(carrierFile), []byte("bed-local"), 0); err != nil {
		t.Fatal(err)
	}
	data, err = reader.Read(carrierFile)
	if err != nil || string(data) != "bed-local" {
		t.Fatalf("primary must win: Read = %q, %v", data, err)
	}
}

func TestReaderDoesNotFallbackRelativePaths(t *testing.T) {
	bedHome := t.TempDir()
	if err := os.Mkdir(filepath.Join(bedHome, "workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	primary, err := New(bedHome)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = primary.Close() })

	fallbackRoot := t.TempDir()
	const name = "carrier-only.txt"
	if err := os.WriteFile(filepath.Join(fallbackRoot, name), []byte("carrier"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewReader(primary, fallbackRoot).Read(name); !os.IsNotExist(err) {
		t.Fatalf("relative path unexpectedly fell back: %v", err)
	}
}
