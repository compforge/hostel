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
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bedfs"
)

func testRestic(t *testing.T) *resticCommand {
	t.Helper()
	binary := os.Getenv("HOSTEL_TEST_RESTIC_BINARY")
	if binary == "" {
		var err error
		binary, err = exec.LookPath("restic")
		if err != nil {
			t.Skip("restic executable unavailable; set HOSTEL_TEST_RESTIC_BINARY")
		}
	}
	r := newResticCommand(Config{ResticBinary: binary})
	if err := r.available(t.Context()); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestResticDirectoryReferencesAndMetadata(t *testing.T) {
	r := testRestic(t)
	repo := filepath.Join(t.TempDir(), "repository")
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "run"), []byte("first"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("run", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	first, err := r.upload(t.Context(), repo, source, "")
	if err != nil {
		t.Fatal(err)
	}
	if !resticRefPattern.MatchString(first.SnapshotID) || first.Files != 1 || first.Bytes != 5 {
		t.Fatalf("summary: %+v", first)
	}
	if err := os.WriteFile(filepath.Join(source, "run"), []byte("second"), 0o755); err != nil {
		t.Fatal(err)
	}
	second, err := r.upload(t.Context(), repo, source, first.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	if second.SnapshotID == first.SnapshotID {
		t.Fatal("changed content reused reference")
	}
	parentJSON, err := r.run(t.Context(), repo, "", "cat", "snapshot", second.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Parent string `json:"parent"`
	}
	if err := json.Unmarshal(parentJSON, &snapshot); err != nil || snapshot.Parent != first.SnapshotID {
		t.Fatalf("parent not honored: %+v %v", snapshot, err)
	}
	if _, err := r.upload(t.Context(), repo, source, strings.Repeat("f", 64)); err == nil {
		t.Fatal("unknown parent silently ignored")
	}
	for _, tc := range []struct{ ref, want string }{{first.SnapshotID, "first"}, {second.SnapshotID, "second"}} {
		dir := t.TempDir()
		if err := r.download(t.Context(), repo, tc.ref, dir); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "run"))
		if err != nil || string(data) != tc.want {
			t.Fatalf("ref=%s data=%q err=%v", tc.ref, data, err)
		}
		info, err := os.Stat(filepath.Join(dir, "run"))
		if err != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("permissions: %v %v", info, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "empty")); err != nil {
			t.Fatal(err)
		}
		if link, err := os.Readlink(filepath.Join(dir, "link")); err != nil || link != "run" {
			t.Fatalf("link=%q %v", link, err)
		}
	}
	if err := r.download(t.Context(), repo, strings.Repeat("0", 64), t.TempDir()); err == nil {
		t.Fatal("unknown ref succeeded")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.upload(ctx, repo, source, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestResticCancellationJoinsProcess(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	stopped := filepath.Join(dir, "stopped")
	binary := filepath.Join(dir, "restic")
	script := "#!/bin/sh\ntrap 'echo stopped > " + stopped + "; exit 130' INT\necho started > " + started + "\nwhile :; do :; done\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r := newResticCommand(Config{ResticBinary: binary})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := r.run(ctx, "unused", "", "backup", "."); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("process did not start")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("cancellation did not join")
	}
	if _, err := os.Stat(stopped); err != nil {
		t.Fatalf("did not allow graceful repository cleanup: %v", err)
	}
}

func TestResticTransferValidation(t *testing.T) {
	req := TransferRequest{ID: "one", Sync: SyncRestic, Source: TransferEndpoint{Type: "bed", Path: "/workspace"}, Destination: TransferEndpoint{Type: "s3", Key: "repositories/one"}}
	if _, err := validateTransfer(req); err != nil {
		t.Fatal(err)
	}
	req.ParentRef = strings.Repeat("a", 64)
	if _, err := validateTransfer(req); err != nil {
		t.Fatal(err)
	}
	req.Sync = SyncCopy
	if _, err := validateTransfer(req); !errors.Is(err, ErrTransferInvalid) {
		t.Fatalf("copy accepted parent: %v", err)
	}
	req.Sync = SyncRestic
	req.ParentRef = ""
	req.Source, req.Destination = req.Destination, req.Source
	if _, err := validateTransfer(req); !errors.Is(err, ErrTransferInvalid) {
		t.Fatal("download accepted missing ref")
	}
	req.Source.Ref = strings.Repeat("b", 64)
	if _, err := validateTransfer(req); err != nil {
		t.Fatal(err)
	}
	req.Sync = SyncNoop
	if _, err := validateTransfer(req); !errors.Is(err, ErrTransferInvalid) {
		t.Fatal("explicit noop accepted")
	}
}

func TestResticImportPreservesUnrelatedFiles(t *testing.T) {
	r := testRestic(t)
	source := t.TempDir()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.WriteFile(filepath.Join(source, "run"), []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	summary, err := r.upload(t.Context(), repo, source, "")
	if err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	if err := r.download(t.Context(), repo, summary.SnapshotID, staging); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	fs, err := bedfs.New(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	if err := fs.Write("/workspace/keep", []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fs.ImportTransferTree(t.Context(), staging, "/workspace", false, nil); err != nil {
		t.Fatal(err)
	}
	if err := fs.ImportTransferTree(t.Context(), staging, "/workspace", false, nil); !errors.Is(err, os.ErrExist) {
		t.Fatalf("conflict: %v", err)
	}
	if err := fs.ImportTransferTree(t.Context(), staging, "/workspace", true, nil); err != nil {
		t.Fatal(err)
	}
	if data, err := fs.Read("/workspace/keep"); err != nil || string(data) != "keep" {
		t.Fatalf("unrelated file changed: %q %v", data, err)
	}
}
