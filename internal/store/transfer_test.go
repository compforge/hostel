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
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bedfs"
)

type transferMemory struct {
	objects map[string]string
	keys    []string
}

func (m *transferMemory) get(_ context.Context, key string) (io.ReadCloser, error) {
	if value, ok := m.objects[key]; ok {
		return io.NopCloser(strings.NewReader(value)), nil
	}
	return nil, os.ErrNotExist
}
func (m *transferMemory) visit(_ context.Context, prefix string, visit func(string) error) error {
	keys := m.keys
	if keys == nil {
		for key := range m.objects {
			if strings.HasPrefix(key, prefix) {
				keys = append(keys, key)
			}
		}
	}
	for _, key := range keys {
		if err := visit(key); err != nil {
			return err
		}
	}
	return nil
}
func (m *transferMemory) upload(_ context.Context, key string, f *os.File, _ int64, overwrite bool) error {
	if _, ok := m.objects[key]; ok && !overwrite {
		return ErrTransferConflict
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	m.objects[key] = string(data)
	return nil
}

func TestTransferCopyRoundTripAndNoDeletion(t *testing.T) {
	fs, err := bedfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	if err := fs.Write("/workspace/source/nested/file", []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	remote := &transferMemory{objects: map[string]string{"prefix/.transfers/export/extra": "keep"}}
	request := TransferRequest{Source: TransferEndpoint{Type: "bed", Path: "/workspace/source"}, Destination: TransferEndpoint{Type: "s3", Key: "export/"}}
	count := int64(0)
	if err := copyTransfer(t.Context(), remote, "prefix/.transfers", fs, request, func(n int64) { count += n }); err != nil {
		t.Fatal(err)
	}
	if count != 7 || remote.objects["prefix/.transfers/export/nested/file"] != "payload" || remote.objects["prefix/.transfers/export/extra"] != "keep" {
		t.Fatalf("copy: %v bytes=%d", remote.objects, count)
	}
	request.Source = TransferEndpoint{Type: "s3", Key: "export/"}
	request.Destination = TransferEndpoint{Type: "bed", Path: "/workspace/destination"}
	if err := copyTransfer(t.Context(), remote, "prefix/.transfers", fs, request, func(int64) {}); err != nil {
		t.Fatal(err)
	}
	data, err := fs.Read("/workspace/destination/nested/file")
	if err != nil || string(data) != "payload" {
		t.Fatalf("roundtrip: %q %v", data, err)
	}
	if err := copyTransfer(t.Context(), remote, "prefix/.transfers", fs, request, func(int64) {}); !errors.Is(err, ErrTransferConflict) {
		t.Fatalf("overwrite conflict: %v", err)
	}
}

func TestTransferRejectsRemoteTraversal(t *testing.T) {
	for _, relative := range []string{"../escape", "a/../../escape", "/absolute", "a//b", "./file", "a\\b"} {
		t.Run(relative, func(t *testing.T) {
			fs, err := bedfs.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer fs.Close()
			remote := &transferMemory{objects: map[string]string{}, keys: []string{"prefix/in/" + relative}}
			req := TransferRequest{Source: TransferEndpoint{Type: "s3", Key: "in/"}, Destination: TransferEndpoint{Type: "bed", Path: "/workspace"}}
			if err := copyTransfer(t.Context(), remote, "prefix", fs, req, func(int64) {}); !errors.Is(err, ErrTransferInvalid) {
				t.Fatalf("unsafe remote path: %v", err)
			}
		})
	}
}

func TestTransferAdmissionAndRetention(t *testing.T) {
	s := NewManagerWithStores(Noop{})
	req := TransferRequest{ID: "one", Source: TransferEndpoint{Type: "bed", Path: "/workspace"}, Destination: TransferEndpoint{Type: "s3", Key: "out/"}}
	acquire := func() (*bedfs.FS, func(), error) { t.Fatal("unexpected Bed acquisition"); return nil, nil, nil }
	if _, err := s.StartTransfer(t.Context(), "bed", req, acquire); !errors.Is(err, ErrTransferUnavailable) {
		t.Fatalf("unconfigured S3: %v", err)
	}
	req.InstanceID = "previous-instance"
	if _, err := s.StartTransfer(t.Context(), "bed", req, acquire); !errors.Is(err, ErrTransferConflict) {
		t.Fatalf("stale instance: %v", err)
	}
	req.InstanceID = ""
	for _, key := range []string{"", "/absolute", "../escape", "a/../b", "a//b"} {
		invalid := req
		invalid.Destination.Key = key
		if _, err := s.StartTransfer(t.Context(), "bed", invalid, acquire); !errors.Is(err, ErrTransferInvalid) {
			t.Fatalf("key %q: %v", key, err)
		}
	}
	now := time.Now()
	old := now.Add(-transferRetention - time.Second)
	s.transfers.runs["bed\x00expired"] = &transferRun{status: Transfer{State: TransferSucceeded, FinishedAt: &old}}
	s.transfers.runs["bed\x00recent"] = &transferRun{status: Transfer{State: TransferSucceeded, FinishedAt: &now}}
	if _, err := s.TransferStatus("bed", "expired"); !errors.Is(err, ErrTransferNotFound) {
		t.Fatalf("expired: %v", err)
	}
	if _, err := s.TransferStatus("bed", "recent"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransferStatus("other", "recent"); !errors.Is(err, ErrTransferNotFound) {
		t.Fatalf("bed scope: %v", err)
	}
	for i := 0; i < transferLimit; i++ {
		s.transfers.runs[string(rune('a'+i))] = &transferRun{status: Transfer{State: TransferRunning}}
	}
	if _, err := s.StartTransfer(t.Context(), "bed", req, acquire); !errors.Is(err, ErrTransferCapacity) {
		t.Fatalf("capacity: %v", err)
	}
}

func TestStopCompletedTransfersDoesNotWaitOnCanceledContext(t *testing.T) {
	s := NewManagerWithStores(Noop{})
	finished := time.Now()
	s.transfers.runs["bed\x00done"] = &transferRun{status: Transfer{BedID: "bed", State: TransferSucceeded, FinishedAt: &finished}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.StopTransfers(ctx, ""); err != nil {
		t.Fatalf("completed work should not need a join: %v", err)
	}
	if _, err := s.CancelTransfer("bed", "done", ""); err != nil {
		t.Fatalf("cancel terminal: %v", err)
	}
}
