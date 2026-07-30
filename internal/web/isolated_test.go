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
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

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

	events := runIsolated(t, s, id, `{"code":"export HOSTEL_ISOLATED_TEST_STATE=kept"}`)
	if events[len(events)-1].Type != EventComplete {
		t.Fatalf("first run events = %+v", events)
	}
	events = runIsolated(t, s, id, `{"code":"echo \"$HOSTEL_ISOLATED_TEST_STATE\""}`)
	if !isolatedOutputContains(events, "kept") || events[len(events)-1].Type != EventComplete {
		t.Fatalf("stateful run events = %+v", events)
	}

	events = runIsolated(t, s, id,
		`{"code":"echo \"$HOSTEL_ISOLATED_REQUEST_ENV\"","envs":{"HOSTEL_ISOLATED_REQUEST_ENV":"scoped"}}`)
	if !isolatedOutputContains(events, "scoped") {
		t.Fatalf("request env run events = %+v", events)
	}
	events = runIsolated(t, s, id, `{"code":"echo \"${HOSTEL_ISOLATED_REQUEST_ENV-unset}\""}`)
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

	rec = do(t, s, http.MethodGet, "/v1/isolated/session/any/diff", nil, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("isolated diff = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s, http.MethodPost, "/v1/isolated/session/any/commit", nil, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("isolated commit = %d %s", rec.Code, rec.Body.String())
	}
}

func TestIsolatedDeleteDefaultBedIsBadRequest(t *testing.T) {
	s := newTestServer(t)
	if _, err := s.mgr.Resolve(""); err != nil {
		t.Fatalf("resolve default bed: %v", err)
	}

	rec := do(t, s, http.MethodDelete, "/v1/isolated/session/default", nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete default isolated session = %d %s", rec.Code, rec.Body.String())
	}
	var response ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if response.Code != ErrInvalidRequest {
		t.Fatalf("delete default error = %+v", response)
	}
}
