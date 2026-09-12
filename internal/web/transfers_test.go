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

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
	"github.com/qiankunli/hostel/internal/bed/store"
)

type transferS3Fixture struct {
	mu       sync.Mutex
	objects  map[string][]byte
	metadata map[string]map[string]string
	writes   int
	requests []string
	blockKey string
	entered  chan struct{}
}

func (f *transferS3Fixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == f.blockKey && r.Method == http.MethodGet {
		select {
		case f.entered <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	switch r.Method {
	case http.MethodHead:
		if r.URL.Path == "/bucket/" || r.URL.Path == "/bucket" {
			return
		}
		data, ok := f.objects[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.Header().Set("Last-Modified", time.Unix(1700000000, 0).UTC().Format(http.TimeFormat))
		w.Header().Set("ETag", `"fixture"`)
		for key, value := range f.metadata[r.URL.Path] {
			w.Header().Set(key, value)
		}
	case http.MethodDelete:
		delete(f.objects, r.URL.Path)
		w.WriteHeader(204)
	case http.MethodPost:
		var request struct {
			Objects []struct {
				Key string `xml:"Key"`
			} `xml:"Object"`
		}
		if err := xml.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(400)
			return
		}
		for _, object := range request.Objects {
			delete(f.objects, "/bucket/"+object.Key)
		}
		fmt.Fprint(w, "<DeleteResult/>")
	case http.MethodPut:
		if _, ok := f.objects[r.URL.Path]; ok && r.Header.Get("If-None-Match") == "*" {
			w.WriteHeader(http.StatusPreconditionFailed)
			fmt.Fprint(w, "<Error><Code>PreconditionFailed</Code></Error>")
			return
		}
		var body io.Reader = r.Body
		// MinIO's client signs streaming chunks inside the HTTP request body.
		// Real S3 removes this framing before storing the object.
		if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") || strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
			body = httputil.NewChunkedReader(body)
		}
		data, err := io.ReadAll(body)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		f.objects[r.URL.Path] = data
		if f.metadata == nil {
			f.metadata = make(map[string]map[string]string)
		}
		f.metadata[r.URL.Path] = make(map[string]string)
		for key := range r.Header {
			if strings.HasPrefix(key, "X-Amz-Meta-") {
				f.metadata[r.URL.Path][key] = r.Header.Get(key)
			}
		}
		f.writes++
		w.Header().Set("ETag", `"fixture"`)
	case http.MethodGet:
		if r.URL.Query().Has("location") {
			fmt.Fprint(w, `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
			return
		}
		if r.URL.Query().Get("list-type") == "2" {
			type object struct {
				Key          string `xml:"Key"`
				Size         int    `xml:"Size"`
				LastModified string `xml:"LastModified"`
				ETag         string `xml:"ETag"`
			}
			result := struct {
				XMLName     xml.Name `xml:"ListBucketResult"`
				IsTruncated bool     `xml:"IsTruncated"`
				Contents    []object `xml:"Contents"`
			}{}
			for key, data := range f.objects {
				key = strings.TrimPrefix(key, "/bucket/")
				if strings.HasPrefix(key, r.URL.Query().Get("prefix")) {
					result.Contents = append(result.Contents, object{Key: key, Size: len(data), LastModified: "2026-01-01T00:00:00Z", ETag: `"fixture"`})
				}
			}
			w.Header().Set("Content-Type", "application/xml")
			_ = xml.NewEncoder(w).Encode(result)
			return
		}
		data, ok := f.objects[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			fmt.Fprint(w, "<Error><Code>NoSuchKey</Code></Error>")
			return
		}
		http.ServeContent(w, r, "object", time.Unix(1700000000, 0), bytes.NewReader(data))
	default:
		w.WriteHeader(405)
	}
}

func newTransferTestServer(t *testing.T, fixture *transferS3Fixture) (*Server, *bed.Resident) {
	t.Helper()
	remote := httptest.NewServer(fixture)
	t.Cleanup(remote.Close)
	stores, err := store.NewManager(t.Context(), store.Config{Sync: "noop", ResticBinary: os.Getenv("HOSTEL_TEST_RESTIC_BINARY"), Bucket: "bucket", Prefix: "hostel", Endpoint: remote.URL, PathStyle: true, Region: "us-east-1", AccessKeyID: "test", SecretAccessKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	mgr, err := bed.NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, stores)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := mgr.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	b, err := mgr.Ensure(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(mgr), b
}

func startTestTransfer(t *testing.T, s *Server, req transferRequest, want int) transferResponse {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	response := do(t, s, http.MethodPost, "/v1/beds/one/transfers", bytes.NewReader(data), map[string]string{"Content-Type": "application/json"})
	if response.Code != want {
		t.Fatalf("start: %d %s", response.Code, response.Body.String())
	}
	var result transferResponse
	if want < 300 {
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func awaitTestTransfer(t *testing.T, s *Server, id string) transferResponse {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		response := do(t, s, http.MethodGet, "/v1/beds/one/transfers/"+id, nil, nil)
		if response.Code != 200 {
			t.Fatalf("status: %d %s", response.Code, response.Body.String())
		}
		var result transferResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.State != store.TransferRunning {
			return result
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("transfer did not finish")
	return transferResponse{}
}

func TestTransfersHTTPRoundTripNoopReplayAndScope(t *testing.T) {
	fixture := &transferS3Fixture{objects: make(map[string][]byte)}
	t.Cleanup(func() {
		if t.Failed() {
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			t.Logf("S3 requests: %v", fixture.requests)
		}
	})
	s, b := newTransferTestServer(t, fixture)
	if err := b.BedFS().Write("/workspace/source/nested/file", []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := transferRequest{ID: "upload", Source: transferEndpoint{Type: "bed", Path: "/workspace/source"}, Destination: transferEndpoint{Type: "s3", Key: "export/"}}
	initial := startTestTransfer(t, s, req, 202)
	result := awaitTestTransfer(t, s, req.ID)
	if result.State != store.TransferSucceeded || result.Files != 1 || result.Bytes != 7 || b.Spec().Sync != store.SyncNoop {
		t.Fatalf("upload: %+v sync=%s", result, b.Spec().Sync)
	}
	req.InstanceID = initial.InstanceID
	replay := startTestTransfer(t, s, req, 200)
	if replay.StartedAt != result.StartedAt {
		t.Fatal("replay created new operation")
	}
	fixture.mu.Lock()
	writes := fixture.writes
	fixture.mu.Unlock()
	if writes != 1 {
		t.Fatalf("duplicate upload: %d", writes)
	}
	staleCancel := do(t, s, http.MethodDelete, "/v1/beds/one/transfers/upload?instance_id=previous", nil, nil)
	if staleCancel.Code != 409 {
		t.Fatalf("stale cancel: %d", staleCancel.Code)
	}
	req.Overwrite = true
	startTestTransfer(t, s, req, 409)
	req.Overwrite = false
	req.InstanceID = "old-instance"
	req.ID = "stale"
	startTestTransfer(t, s, req, 409)
	req.InstanceID = ""
	req.ID = "conflict"
	startTestTransfer(t, s, req, 202)
	if got := awaitTestTransfer(t, s, req.ID); got.State != store.TransferFailed || got.Error != "destination already exists" {
		t.Fatalf("overwrite=false: %+v", got)
	}
	req = transferRequest{ID: "download", Source: transferEndpoint{Type: "s3", Key: "export/"}, Destination: transferEndpoint{Type: "bed", Path: "/workspace/destination"}}
	startTestTransfer(t, s, req, 202)
	if got := awaitTestTransfer(t, s, req.ID); got.State != store.TransferSucceeded {
		t.Fatalf("download: %+v", got)
	}
	data, err := b.BedFS().Read("/workspace/destination/nested/file")
	if err != nil || string(data) != "payload" {
		t.Fatalf("roundtrip %q %v", data, err)
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		response := do(t, s, method, "/v1/beds/other/transfers/upload", nil, nil)
		if response.Code != 404 || response.Header().Get("X-Hostel-Transfer-Instance") == "" {
			t.Fatalf("scope/instance: %d %s", response.Code, response.Body.String())
		}
	}
	if _, ok := s.mgr.Get("other"); ok {
		t.Fatal("query created a Bed")
	}
	if err := s.mgr.Purge(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	uploaded := string(fixture.objects["/bucket/hostel/export/nested/file"])
	fixture.mu.Unlock()
	if uploaded != "payload" {
		t.Fatal("purge removed copied objects")
	}
	if response := do(t, s, http.MethodGet, "/v1/beds/one/transfers/upload", nil, nil); response.Code != 200 {
		t.Fatal("lost terminal state after purge")
	}
}

func TestTransfersCancelAndPurgeJoinBeforeBedRemoval(t *testing.T) {
	for _, action := range []string{"cancel", "purge", "shutdown", "timeout"} {
		t.Run(action, func(t *testing.T) {
			fixture := &transferS3Fixture{objects: make(map[string][]byte), blockKey: "/bucket/hostel/block", entered: make(chan struct{}, 1)}
			s, b := newTransferTestServer(t, fixture)
			req := transferRequest{ID: "blocked", Source: transferEndpoint{Type: "s3", Key: "block"}, Destination: transferEndpoint{Type: "bed", Path: "/workspace/result"}}
			if action == "timeout" {
				req.TimeoutMs = 1000
			}
			startTestTransfer(t, s, req, 202)
			select {
			case <-fixture.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("remote not reached")
			}
			if b.Status().Activity != bed.ActivityActive {
				t.Fatalf("transfer did not pin Bed: %+v", b.Status())
			}
			if evicted, err := s.mgr.Evict(t.Context(), "one"); err != nil || evicted {
				t.Fatalf("evicted running transfer: %v %v", evicted, err)
			}
			switch action {
			case "cancel":
				response := do(t, s, http.MethodDelete, "/v1/beds/one/transfers/blocked", nil, nil)
				if response.Code != 202 {
					t.Fatalf("cancel: %d %s", response.Code, response.Body.String())
				}
			case "purge":
				if err := s.mgr.Purge(t.Context(), "one"); err != nil {
					t.Fatal(err)
				}
			case "shutdown":
				if err := s.mgr.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			got := awaitTestTransfer(t, s, req.ID)
			if action == "timeout" {
				if got.State != store.TransferFailed || got.Error != "deadline exceeded" {
					t.Fatalf("timeout: %+v", got)
				}
			} else if got.State != store.TransferCanceled {
				t.Fatalf("terminal: %+v", got)
			}
		})
	}
}

func TestTransfersWithoutS3AndInvalidTimeout(t *testing.T) {
	s := newTestServer(t)
	req := transferRequest{ID: "one", Source: transferEndpoint{Type: "bed", Path: "/workspace"}, Destination: transferEndpoint{Type: "s3", Key: "export/"}}
	startTestTransfer(t, s, req, 503)
	req.TimeoutMs = 1 << 62
	startTestTransfer(t, s, req, 400)
	req.TimeoutMs = 0
	req.Destination.Key = "../escape"
	startTestTransfer(t, s, req, 400)
}

func TestTransferConcurrentReplaySurvivesRequestCancellation(t *testing.T) {
	fixture := &transferS3Fixture{objects: make(map[string][]byte), blockKey: "/bucket/hostel/block", entered: make(chan struct{}, 10)}
	s, b := newTransferTestServer(t, fixture)
	req := transferRequest{ID: "same", Source: transferEndpoint{Type: "s3", Key: "block"}, Destination: transferEndpoint{Type: "bed", Path: "/workspace/result"}}
	data, _ := json.Marshal(req)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			ctx, cancel := context.WithCancel(t.Context())
			request := httptest.NewRequest(http.MethodPost, "/v1/beds/one/transfers", bytes.NewReader(data)).WithContext(ctx)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			s.Handler().ServeHTTP(response, request)
			cancel()
			if response.Code != 202 {
				t.Errorf("concurrent replay: %d %s", response.Code, response.Body.String())
			}
		})
	}
	wg.Wait()
	select {
	case <-fixture.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("remote not reached")
	}
	if got := do(t, s, http.MethodGet, "/v1/beds/one/transfers/same", nil, nil); !strings.Contains(got.Body.String(), `"state":"running"`) {
		t.Fatalf("request cancellation stopped copy: %s", got.Body.String())
	}
	if b.Status().Activity != bed.ActivityActive {
		t.Fatal("replayed transfer lost Bed pin")
	}
	do(t, s, http.MethodDelete, "/v1/beds/one/transfers/same", nil, nil)
	if result := awaitTestTransfer(t, s, "same"); result.State != store.TransferCanceled {
		t.Fatalf("cancel: %+v", result)
	}
	if len(fixture.entered) != 0 {
		t.Fatal("concurrent requests launched multiple downloads")
	}
}

func TestResticTransfersHTTPReferenceAndNoopBed(t *testing.T) {
	if os.Getenv("HOSTEL_TEST_RESTIC_BINARY") == "" {
		if _, err := exec.LookPath("restic"); err != nil {
			t.Skip("restic executable unavailable")
		}
	}
	fixture := &transferS3Fixture{objects: make(map[string][]byte)}
	t.Cleanup(func() {
		if t.Failed() {
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			t.Logf("S3 requests: %v", fixture.requests)
		}
	})
	s, b := newTransferTestServer(t, fixture)
	if err := b.BedFS().Write("/workspace/source/run", []byte("first"), 0o755); err != nil {
		t.Fatal(err)
	}
	req := transferRequest{Sync: store.SyncRestic, ID: "restic-upload", Source: transferEndpoint{Type: "bed", Path: "/workspace/source"}, Destination: transferEndpoint{Type: "s3", Key: "tenants/test/repositories/one"}}
	first := startTestTransfer(t, s, req, 202)
	result := awaitTestTransfer(t, s, req.ID)
	if result.State != store.TransferSucceeded || len(result.Ref) != 64 || result.Sync != store.SyncRestic || b.Spec().Sync != store.SyncNoop {
		t.Fatalf("restic upload: %+v", result)
	}
	firstRef := result.Ref
	req.InstanceID = first.InstanceID
	if replay := startTestTransfer(t, s, req, 200); replay.Ref != firstRef {
		t.Fatal("replay lost result")
	}
	if err := b.BedFS().Write("/workspace/source/run", []byte("second"), 0o755); err != nil {
		t.Fatal(err)
	}
	req.ID = "restic-incremental"
	req.ParentRef = firstRef
	startTestTransfer(t, s, req, 202)
	second := awaitTestTransfer(t, s, req.ID)
	if second.State != store.TransferSucceeded || second.Ref == firstRef {
		t.Fatalf("incremental: %+v", second)
	}
	req = transferRequest{Sync: store.SyncRestic, ID: "restic-download", Source: transferEndpoint{Type: "s3", Key: "tenants/test/repositories/one", Ref: firstRef}, Destination: transferEndpoint{Type: "bed", Path: "/workspace/target"}}
	startTestTransfer(t, s, req, 202)
	result = awaitTestTransfer(t, s, req.ID)
	if result.State != store.TransferSucceeded || result.Ref != firstRef {
		t.Fatalf("download: %+v", result)
	}
	data, err := b.BedFS().Read("/workspace/target/run")
	if err != nil || string(data) != "first" {
		t.Fatalf("exact reference: %q %v", data, err)
	}
	info, err := b.BedFS().Stat("/workspace/target/run")
	if err != nil || info.Mode != 0o755 {
		t.Fatalf("metadata: %+v %v", info, err)
	}
	// Explicit repositories are addressed directly, so existing hictld layouts
	// can be read without a hidden transfer-specific prefix.
	fixture.mu.Lock()
	_, exists := fixture.objects["/bucket/hostel/tenants/test/repositories/one/config"]
	fixture.mu.Unlock()
	if !exists {
		t.Fatal("repository key was rewritten")
	}
}

func TestResticAutomaticSyncAndFormatSwitch(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	binary := os.Getenv("HOSTEL_TEST_RESTIC_BINARY")
	if binary == "" {
		var err error
		binary, err = exec.LookPath("restic")
		if err != nil {
			t.Skip("restic executable unavailable")
		}
	}
	fixture := &transferS3Fixture{objects: make(map[string][]byte)}
	remote := httptest.NewServer(fixture)
	defer remote.Close()
	t.Cleanup(func() {
		if t.Failed() {
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			t.Logf("S3 requests: %v", fixture.requests)
		}
	})
	manager, err := store.NewManager(t.Context(), store.Config{Sync: "auto", ResticBinary: binary, Bucket: "bucket", Prefix: "hostel", Endpoint: remote.URL, PathStyle: true, Region: "us-east-1", AccessKeyID: "test", SecretAccessKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data/workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"version":1,"bed_id":"automatic","sync":"restic"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data/workspace/run"), []byte("first"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "data/tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data/tmp/volatile"), []byte("skip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Persist(ctx, store.SyncRestic, "automatic", dir, 1); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []store.SyncKind{store.SyncCAS, store.SyncAuto, store.SyncRestic} {
		target := filepath.Join(t.TempDir(), "restored")
		result, err := manager.StageInBedFS(ctx, kind, store.StageInRequest{BedID: "automatic", BedDir: target})
		if err != nil || !result.Restored || result.Snapshot.Generation != 1 {
			t.Fatalf("restore %s: %+v %v", kind, result, err)
		}
		if data, err := os.ReadFile(filepath.Join(target, "data/workspace/run")); err != nil || string(data) != "first" {
			t.Fatalf("restored: %q %v", data, err)
		}
		if _, err := os.Stat(filepath.Join(target, "data/tmp/volatile")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("volatile data persisted: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "data/workspace/run"), []byte("second"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := manager.Persist(ctx, store.SyncTar, "automatic", dir, 2); err != nil {
		t.Fatal(err)
	}
	if err := manager.Persist(ctx, store.SyncRestic, "automatic", dir, 2); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("generation conflict: %v", err)
	}
	target := filepath.Join(t.TempDir(), "newest")
	result, err := manager.StageInBedFS(ctx, store.SyncRestic, store.StageInRequest{BedID: "automatic", BedDir: target})
	if err != nil || result.Snapshot.Generation != 2 {
		t.Fatalf("format switch: %+v %v", result, err)
	}
	if data, err := os.ReadFile(filepath.Join(target, "data/workspace/run")); err != nil || string(data) != "second" {
		t.Fatalf("newest: %q %v", data, err)
	}
	if err := manager.Delete(t.Context(), store.SyncAuto, "automatic"); err != nil {
		t.Fatal(err)
	}
	if info, err := manager.Stat(t.Context(), store.SyncAuto, "automatic"); err != nil || info != nil {
		t.Fatalf("purge: %+v %v", info, err)
	}
}
