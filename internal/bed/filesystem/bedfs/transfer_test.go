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
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransferFilesStayConfinedAndPublishAtomically(t *testing.T) {
	home := t.TempDir()
	fs, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	if _, err := fs.WriteTransferFile(t.Context(), "/workspace/nested/file", strings.NewReader("first"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteTransferFile(t.Context(), "/workspace/nested/file", strings.NewReader("second"), false); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite=false: %v", err)
	}
	data, _ := fs.Read("/workspace/nested/file")
	if string(data) != "first" {
		t.Fatalf("existing file changed: %q", data)
	}
	if _, err := fs.WriteTransferFile(t.Context(), "/workspace/nested/file", failingTransferReader{}, true); err == nil {
		t.Fatal("expected interrupted body failure")
	}
	data, _ = fs.Read("/workspace/nested/file")
	if string(data) != "first" {
		t.Fatalf("partial file published: %q", data)
	}
	if _, err := fs.WriteTransferFile(t.Context(), "/workspace/nested/file", strings.NewReader("second"), true); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(home, "workspace/nested"))
	if len(entries) != 1 {
		t.Fatalf("temporary files leaked: %v", entries)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(home, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteTransferFile(t.Context(), "/escape/file", strings.NewReader("escape"), true); err == nil {
		t.Fatal("write escaped BedFS")
	}
	if _, err := os.Stat(filepath.Join(outside, "file")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside file: %v", err)
	}
}

type failingTransferReader struct{}

func (failingTransferReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestTransferWalkRejectsSymlinkAndPreservesRelativePaths(t *testing.T) {
	home := t.TempDir()
	fs, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	if err := fs.Write("/workspace/nested/file", []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	visits := 0
	err = fs.WalkTransferFiles(t.Context(), "/workspace", func(relative string, f *os.File, size int64) error {
		visits++
		data, err := io.ReadAll(f)
		if relative != "nested/file" || size != 7 || string(data) != "payload" {
			t.Fatalf("walk: %s %d %q", relative, size, data)
		}
		return err
	})
	if err != nil || visits != 1 {
		t.Fatalf("visits=%d err=%v", visits, err)
	}
	if err := os.Symlink("nested/file", filepath.Join(home, "workspace/link")); err != nil {
		t.Fatal(err)
	}
	if err := fs.WalkTransferFiles(t.Context(), "/workspace/link", func(string, *os.File, int64) error { t.Fatal("followed symlink"); return nil }); !errors.Is(err, ErrTransferFileType) {
		t.Fatalf("symlink: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fs.WriteTransferFile(ctx, "/workspace/canceled", strings.NewReader("payload"), false); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write: %v", err)
	}
	if _, err := fs.Stat("/workspace/canceled"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled file published: %v", err)
	}
}

func TestTransferOverwritePreservesPermissions(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o755} {
		t.Run(mode.String(), func(t *testing.T) {
			home := t.TempDir()
			fs, err := New(home)
			if err != nil {
				t.Fatal(err)
			}
			defer fs.Close()
			const target = "/workspace/file"
			if err := fs.Write(target, []byte("original"), int(mode)); err != nil {
				t.Fatal(err)
			}
			for _, interrupted := range []bool{true, false} {
				var body io.Reader = strings.NewReader("replacement")
				if interrupted {
					body = failingTransferReader{}
				}
				_, err := fs.WriteTransferFile(t.Context(), target, body, true)
				if (err != nil) != interrupted {
					t.Fatalf("interrupted=%v: %v", interrupted, err)
				}
				info, err := os.Stat(filepath.Join(home, "workspace/file"))
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != mode {
					t.Fatalf("mode=%o want=%o", info.Mode().Perm(), mode)
				}
				data, err := fs.Read(target)
				want := "replacement"
				if interrupted {
					want = "original"
				}
				if err != nil || string(data) != want {
					t.Fatalf("data=%q error=%v", data, err)
				}
			}
			if _, err := fs.WriteTransferFile(t.Context(), "/workspace/new", strings.NewReader("new"), true); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(filepath.Join(home, "workspace/new"))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o644 {
				t.Fatalf("new mode=%o", info.Mode().Perm())
			}
		})
	}
}
