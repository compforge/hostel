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
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/isolation"
	"github.com/qiankunli/hostel/internal/store"
)

type isolatedBlockingStore struct {
	store.Noop
	started chan struct{}
	release chan struct{}
}

func (s *isolatedBlockingStore) Persist(ctx context.Context, _ string, _ string, _ int64) error {
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.release:
		return nil
	}
}

func createIsolatedSession(t *testing.T, s *Server) string {
	t.Helper()
	rec := do(t, s, http.MethodPost, "/v1/isolated/session",
		strings.NewReader(`{"profile":"balanced","workspace":{"path":"/workspace","mode":"rw"}}`),
		map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create isolated session = %d %s", rec.Code, rec.Body.String())
	}
	var response isolatedCreateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if response.SessionID == "" || response.CreatedAt.IsZero() {
		t.Fatalf("create response = %+v", response)
	}
	return response.SessionID
}

func runIsolated(t *testing.T, s *Server, sessionID, body string) []StreamEvent {
	t.Helper()
	rec := do(t, s, http.MethodPost, "/v1/isolated/session/"+sessionID+"/run",
		strings.NewReader(body), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Fatalf("run isolated session = %d %s", rec.Code, rec.Body.String())
	}
	return parseSSE(t, rec.Body.String())
}

func TestIsolatedSessionLifecycleAndStatefulRun(t *testing.T) {
	s := newTestServer(t)
	id := createIsolatedSession(t, s)

	rec := do(t, s, http.MethodGet, "/v1/isolated/session/"+id, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get isolated session = %d %s", rec.Code, rec.Body.String())
	}
	var state isolatedSessionState
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode isolated state: %v", err)
	}
	if state.Status != "active" || state.Profile != "balanced" ||
		state.Workspace == nil || state.Workspace.Path != "/workspace" || state.Workspace.Mode != "rw" {
		t.Fatalf("isolated state = %+v", state)
	}

	events := runIsolated(t, s, id, `{"code":"export SESSION_TEST_STATE=kept"}`)
	if events[len(events)-1].Type != EventExecutionEnd {
		t.Fatalf("first run events = %+v", events)
	}
	events = runIsolated(t, s, id, `{"code":"echo \"$SESSION_TEST_STATE\""}`)
	if !isolatedOutputContains(events, "kept") || events[len(events)-1].Type != EventExecutionEnd {
		t.Fatalf("stateful run events = %+v", events)
	}

	events = runIsolated(t, s, id,
		`{"code":"echo \"$REQUEST_TEST_ENV\"","envs":{"REQUEST_TEST_ENV":"scoped"}}`)
	if !isolatedOutputContains(events, "scoped") {
		t.Fatalf("request env run events = %+v", events)
	}
	events = runIsolated(t, s, id, `{"code":"echo \"${REQUEST_TEST_ENV-unset}\""}`)
	if !isolatedOutputContains(events, "unset") {
		t.Fatalf("request env leaked into persistent shell: %+v", events)
	}

	rec = do(t, s, http.MethodGet, "/v1/isolated/sessions", nil, nil)
	var list isolatedListResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil ||
		len(list.Sessions) != 1 || list.Sessions[0].SessionID != id {
		t.Fatalf("list isolated sessions = %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, s, http.MethodDelete, "/v1/isolated/session/"+id, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete isolated session = %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := s.mgr.Get(id); ok {
		t.Fatal("deleted isolated session remains resident")
	}
	rec = do(t, s, http.MethodGet, "/v1/isolated/session/"+id, nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get deleted isolated session = %d %s", rec.Code, rec.Body.String())
	}
}

func TestIsolatedSessionRemainsActiveWhileEvicting(t *testing.T) {
	root := t.TempDir()
	backend := &isolatedBlockingStore{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	mgr, err := bed.NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, backend)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := mgr.Close(ctx); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	const id = "isolated-evicting"
	if _, err := mgr.Ensure(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	s := NewServer(mgr)
	evictDone := make(chan error, 1)
	go func() {
		_, err := mgr.Evict(context.Background(), id)
		evictDone <- err
	}()
	<-backend.started

	rec := do(t, s, http.MethodGet, "/v1/isolated/session/"+id, nil, nil)
	var state isolatedSessionState
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &state) != nil {
		t.Fatalf("get evicting session = %d %s", rec.Code, rec.Body.String())
	}
	if state.Status != "active" {
		t.Fatalf("evicting session status = %q, want active", state.Status)
	}

	close(backend.release)
	if err := <-evictDone; err != nil {
		t.Fatal(err)
	}
}

func isolatedOutputContains(events []StreamEvent, want string) bool {
	for _, event := range events {
		if event.Type == EventStdout && strings.Contains(event.Text, want) {
			return true
		}
	}
	return false
}

func TestIsolatedFilesReuseBedWithoutCreatingMissingSession(t *testing.T) {
	s := newTestServer(t)
	id := createIsolatedSession(t, s)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("metadata", `{"path":"/workspace/isolated.txt"}`)
	file, _ := writer.CreateFormFile("file", "isolated.txt")
	_, _ = file.Write([]byte("isolated bed"))
	_ = writer.Close()

	rec := do(t, s, http.MethodPost, "/v1/isolated/session/"+id+"/files/upload",
		&body, map[string]string{"Content-Type": writer.FormDataContentType()})
	if rec.Code != http.StatusOK {
		t.Fatalf("isolated upload = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s, http.MethodGet,
		"/v1/isolated/session/"+id+"/files/download?path=/workspace/isolated.txt", nil, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "isolated bed" {
		t.Fatalf("isolated download = %d %q", rec.Code, rec.Body.String())
	}

	before := s.mgr.ResidentBedCount()
	rec = do(t, s, http.MethodGet,
		"/v1/isolated/session/missing/files/info?path=/workspace/isolated.txt", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing isolated file session = %d %s", rec.Code, rec.Body.String())
	}
	if after := s.mgr.ResidentBedCount(); after != before {
		t.Fatalf("missing attach created a bed: before=%d after=%d", before, after)
	}
}

func TestIsolatedUnsupportedOptionsAndCapabilities(t *testing.T) {
	s := newTestServer(t)
	rec := do(t, s, http.MethodPost, "/v1/isolated/session",
		strings.NewReader(`{"profile":"strict","workspace":{"path":"/workspace","mode":"overlay"}}`),
		map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported create = %d %s", rec.Code, rec.Body.String())
	}
	var response ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Code != ErrNotSupported {
		t.Fatalf("unsupported response = %+v err=%v", response, err)
	}

	rec = do(t, s, http.MethodGet, "/v1/isolated/capabilities", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("isolated capabilities = %d %s", rec.Code, rec.Body.String())
	}
	var capabilities map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &capabilities); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if capabilities["available"] != true || capabilities["commit_supported"] != false ||
		capabilities["diff_supported"] != false {
		t.Fatalf("isolated capabilities = %v", capabilities)
	}

	if _, err := s.mgr.Ensure(context.Background(), "any"); err != nil {
		t.Fatalf("resolve session for unsupported routes: %v", err)
	}
	rec = do(t, s, http.MethodGet, "/v1/isolated/session/any/diff", nil, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("isolated diff = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s, http.MethodPost, "/v1/isolated/session/any/commit", nil, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("isolated commit = %d %s", rec.Code, rec.Body.String())
	}
}

func TestDefaultBedIsNotAnIsolatedSession(t *testing.T) {
	s := newTestServer(t)
	if _, err := s.mgr.Ensure(context.Background(), ""); err != nil {
		t.Fatalf("resolve default bed: %v", err)
	}
	visible, err := s.mgr.Ensure(context.Background(), "native-bed")
	if err != nil {
		t.Fatalf("resolve non-default bed: %v", err)
	}

	rec := do(t, s, http.MethodGet, "/v1/isolated/sessions", nil, nil)
	var list isolatedListResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil ||
		len(list.Sessions) != 1 || list.Sessions[0].SessionID != visible.ID {
		t.Fatalf("list isolated sessions with default bed = %d %s", rec.Code, rec.Body.String())
	}

	for _, request := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/isolated/session/default"},
		{http.MethodDelete, "/v1/isolated/session/default"},
		{http.MethodGet, "/v1/isolated/session/default/diff"},
		{http.MethodPost, "/v1/isolated/session/default/commit"},
	} {
		rec = do(t, s, request.method, request.path, nil, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d %s", request.method, request.path, rec.Code, rec.Body.String())
		}
	}
	if _, ok := s.mgr.Get(""); !ok {
		t.Fatal("isolated delete removed the default bed")
	}
}
