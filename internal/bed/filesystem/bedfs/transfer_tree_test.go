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
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTransferTreePreservesMetadataAndConfinesLinks(t *testing.T) {
	home := t.TempDir()
	fs, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	if err := fs.Write("/workspace/source/nested/run", []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "workspace/source/empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nested/run", filepath.Join(home, "workspace/source/link")); err != nil {
		t.Fatal(err)
	}
	mtime := time.Unix(1700000000, 0)
	if err := os.Chtimes(filepath.Join(home, "workspace/source/nested/run"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	exported := t.TempDir()
	if err := fs.ExportTransferTree(t.Context(), "/workspace/source", exported, nil); err != nil {
		t.Fatal(err)
	}
	if err := fs.ImportTransferTree(t.Context(), exported, "/workspace/dest", false, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(home, "workspace/dest/nested/run"))
	if err != nil || info.Mode().Perm() != 0o755 || !info.ModTime().Equal(mtime) {
		t.Fatalf("metadata: %v %v", info, err)
	}
	if link, err := os.Readlink(filepath.Join(home, "workspace/dest/link")); err != nil || link != "nested/run" {
		t.Fatalf("symlink: %q %v", link, err)
	}
	if info, err := os.Stat(filepath.Join(home, "workspace/dest/empty")); err != nil || !info.IsDir() {
		t.Fatalf("empty directory: %v %v", info, err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(home, "workspace/source/escape")); err != nil {
		t.Fatal(err)
	}
	if err := fs.ExportTransferTree(t.Context(), "/workspace/source", t.TempDir(), nil); !errors.Is(err, ErrTransferFileType) {
		t.Fatalf("export escaped: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "workspace/redirect")); err != nil {
		t.Fatal(err)
	}
	if err := fs.ImportTransferTree(t.Context(), exported, "/workspace/redirect", true, nil); err == nil {
		t.Fatal("import escaped BedFS")
	}
}
