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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/isolation"
	"github.com/qiankunli/hostel/internal/store"
)

type transferS3Fixture struct {
	mu       sync.Mutex
	objects  map[string][]byte
	writes   int
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
	switch r.Method {
	case http.MethodPut:
		if _, ok := f.objects[r.URL.Path]; ok && r.Header.Get("If-None-Match") == "*" {
			w.WriteHeader(http.StatusPreconditionFailed)
			fmt.Fprint(w, "<Error><Code>PreconditionFailed</Code></Error>")
			return
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		f.objects[r.URL.Path] = data
		f.writes++
		w.Header().Set("ETag", `"fixture"`)
	case http.MethodGet:
		if r.URL.Query().Get("list-type") == "2" {
			type object struct {
				Key string `xml:"Key"`
			}
			result := struct {
				XMLName     xml.Name `xml:"ListBucketResult"`
				IsTruncated bool     `xml:"IsTruncated"`
				Contents    []object `xml:"Contents"`
			}{}
			for key := range f.objects {
				key = strings.TrimPrefix(key, "/bucket/")
				if strings.HasPrefix(key, r.URL.Query().Get("prefix")) {
					result.Contents = append(result.Contents, object{Key: key})
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
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		_, _ = w.Write(data)
	default:
		w.WriteHeader(405)
	}
}

func newTransferTestServer(t *testing.T, fixture *transferS3Fixture) (*Server, *bed.Bed) {
	t.Helper()
	remote := httptest.NewServer(fixture)
	t.Cleanup(remote.Close)
	stores, err := store.NewManager(t.Context(), store.Config{Kind: "noop", Bucket: "bucket", Prefix: "hostel", Endpoint: remote.URL, PathStyle: true, Region: "us-east-1", AccessKeyID: "test", SecretAccessKey: "test"})
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
	deadline := time.Now().Add(5 * time.Second)
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
	s, b := newTransferTestServer(t, fixture)
	if err := b.BedFS().Write("/workspace/source/nested/file", []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := transferRequest{ID: "upload", Source: transferEndpoint{Type: "bed", Path: "/workspace/source"}, Destination: transferEndpoint{Type: "s3", Key: "export/"}}
	initial := startTestTransfer(t, s, req, 202)
	result := awaitTestTransfer(t, s, req.ID)
	if result.State != store.TransferSucceeded || result.Files != 1 || result.Bytes != 7 || b.Store != store.KindNoop {
		t.Fatalf("upload: %+v store=%s", result, b.Store)
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
	uploaded := string(fixture.objects["/bucket/hostel/.transfers/export/nested/file"])
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
			fixture := &transferS3Fixture{objects: make(map[string][]byte), blockKey: "/bucket/hostel/.transfers/block", entered: make(chan struct{}, 1)}
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
	fixture := &transferS3Fixture{objects: make(map[string][]byte), blockKey: "/bucket/hostel/.transfers/block", entered: make(chan struct{}, 10)}
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
