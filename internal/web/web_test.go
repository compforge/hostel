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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/executor"
	"github.com/qiankunli/hostel/internal/isolation"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	mgr, err := bed.NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := mgr.Close(ctx); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	return NewServer(mgr)
}

func do(t *testing.T, s *Server, method, path string, body io.Reader, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func createBedAndWait(t *testing.T, s *Server, id string) *httptest.ResponseRecorder {
	t.Helper()
	rec := do(t, s, http.MethodPost, "/v1/beds", strings.NewReader(`{"id":"`+id+`"}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusOK {
		t.Fatalf("create bed %s = %d %s", id, rec.Code, rec.Body.String())
	}
	return waitBedReady(t, s, id)
}

func waitBedReady(t *testing.T, s *Server, id string) *httptest.ResponseRecorder {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		rec := do(t, s, http.MethodGet, "/v1/beds/"+id, nil, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("get bed %s = %d %s", id, rec.Code, rec.Body.String())
		}
		var view struct {
			Status struct {
				Readiness struct {
					Ready bool `json:"status"`
				} `json:"readiness"`
			} `json:"status"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
			t.Fatalf("decode bed %s: %v", id, err)
		}
		if view.Status.Readiness.Ready {
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("bed %s did not become ready: %s", id, rec.Body.String())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPingAndHealthz(t *testing.T) {
	s := newTestServer(t)
	if rec := do(t, s, "GET", "/ping", nil, nil); rec.Code != 200 || rec.Body.String() != "pong" {
		t.Fatalf("/ping = %d %q", rec.Code, rec.Body.String())
	}
	rec := do(t, s, "GET", "/healthz", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("/healthz = %d", rec.Code)
	}
	var h map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &h)
	iso, _ := h["isolation"].(map[string]any)
	accounting, _ := h["resource_accounting"].(map[string]any)
	if h["ok"] != true || iso == nil || iso["level"] != "dorm" || iso["mechanism"] != "direct" {
		t.Fatalf("/healthz body = %v", h)
	}
	if accounting == nil || accounting["backend"] != "noop" || accounting["available"] != false {
		t.Fatalf("/healthz resource_accounting = %v", accounting)
	}
	if h["beds"] != float64(0) {
		t.Fatalf("/healthz beds = %v, want 0", h["beds"])
	}
	if h["executor_backend"] != "local" || h["spawner"] != nil {
		t.Fatalf("/healthz executor fields = %v", h)
	}
}

func TestUploadInfoDownloadRoundTrip(t *testing.T) {
	s := newTestServer(t)

	// multipart upload with a metadata JSON part carrying the path.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("metadata", `{"path":"/workspace/hi.txt"}`)
	fw, _ := mw.CreateFormFile("file", "hi.txt")
	_, _ = fw.Write([]byte("hello hostel"))
	_ = mw.Close()

	rec := do(t, s, "POST", "/files/upload", &buf, map[string]string{"Content-Type": mw.FormDataContentType()})
	if rec.Code != 200 {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body.String())
	}

	// info returns a map keyed by the requested path.
	rec = do(t, s, "GET", "/files/info?path=/workspace/hi.txt", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("info = %d %s", rec.Code, rec.Body.String())
	}
	var info map[string]map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &info)
	if info["/workspace/hi.txt"]["type"] != "file" {
		t.Fatalf("info body = %v", info)
	}

	// download returns the bytes, with an explicit Content-Length so relays
	// (e.g. to S3 presigned PUT) never see a chunked body of unknown size.
	rec = do(t, s, "GET", "/files/download?path=/workspace/hi.txt", nil, nil)
	if rec.Code != 200 || rec.Body.String() != "hello hostel" {
		t.Fatalf("download = %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len("hello hostel")) {
		t.Fatalf("download Content-Length = %q", got)
	}
}

func TestDormFileAPIsReadCommandAbsolutePathWhenEnabled(t *testing.T) {
	base := newTestServer(t)
	s := NewServer(base.mgr, WithDormReadFallbackRoot("/"))
	rawDir := t.TempDir()
	rawPath := filepath.Join(rawDir, "artifact.txt")
	body, err := json.Marshal(RunCommandRequest{
		Command: "printf 'carrier-first\\nsecond\\n' > " + strconv.Quote(rawPath),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, s, http.MethodPost, "/command", bytes.NewReader(body), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Fatalf("command absolute write = %d %s", rec.Code, rec.Body.String())
	}

	queryPath := url.QueryEscape(filepath.ToSlash(rawPath))
	rec = do(t, s, http.MethodGet, "/files/info?path="+queryPath, nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"path":"`+filepath.ToSlash(rawPath)+`"`) {
		t.Fatalf("fallback info = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s, http.MethodGet, "/files/download?path="+queryPath+"&offset=1&limit=1", nil, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "second\n" {
		t.Fatalf("fallback download = %d %q", rec.Code, rec.Body.String())
	}
	rec = do(t, s, http.MethodGet, "/directories/list?path="+url.QueryEscape(filepath.ToSlash(rawDir)), nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), filepath.ToSlash(rawPath)) {
		t.Fatalf("fallback list = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s, http.MethodGet, "/files/search?path="+url.QueryEscape(filepath.ToSlash(rawDir))+"&pattern=artifact.txt", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), filepath.ToSlash(rawPath)) {
		t.Fatalf("fallback search = %d %s", rec.Code, rec.Body.String())
	}

	b, ok := s.mgr.Get(s.mgr.DefaultBedID())
	if !ok {
		t.Fatal("default bed not found after command")
	}
	if err := b.BedFS().Write(filepath.ToSlash(rawPath), []byte("bed-local"), 0); err != nil {
		t.Fatal(err)
	}
	rec = do(t, s, http.MethodGet, "/files/download?path="+queryPath, nil, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "bed-local" {
		t.Fatalf("bed-local path must win = %d %q", rec.Code, rec.Body.String())
	}
	rec = do(t, s, http.MethodDelete, "/files?path="+queryPath, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete bed-local path = %d %s", rec.Code, rec.Body.String())
	}
	if raw, err := os.ReadFile(rawPath); err != nil || string(raw) != "carrier-first\nsecond\n" {
		t.Fatalf("mutation touched fallback path: content=%q err=%v", raw, err)
	}
}

func TestDormFileAPIsDoNotReadProcessRootByDefault(t *testing.T) {
	s := newTestServer(t)
	rawPath := filepath.Join(t.TempDir(), "carrier-only.txt")
	if err := os.WriteFile(rawPath, []byte("carrier"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := do(t, s, http.MethodGet, "/files/download?path="+url.QueryEscape(filepath.ToSlash(rawPath)), nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled fallback download = %d %s, want 404", rec.Code, rec.Body.String())
	}
}

func TestAbsolutePathAndCommandCwdShareBedHome(t *testing.T) {
	s := newTestServer(t)
	const clientPath = "/tmp/workspace/job/input.txt"

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("metadata", `{"path":"`+clientPath+`"}`)
	fw, _ := mw.CreateFormFile("file", "input.txt")
	_, _ = fw.Write([]byte("bed-local"))
	_ = mw.Close()

	rec := do(t, s, "POST", "/files/upload", &buf, map[string]string{"Content-Type": mw.FormDataContentType()})
	if rec.Code != http.StatusOK {
		t.Fatalf("upload absolute path = %d %s", rec.Code, rec.Body.String())
	}

	// Echo symmetry ("own pod" contract): info reports the path exactly as sent,
	// not rewritten to a /workspace canonical form.
	rec = do(t, s, "GET", "/files/info?path="+clientPath, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("info absolute path = %d %s", rec.Code, rec.Body.String())
	}
	var info map[string]map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &info)
	if info[clientPath]["path"] != clientPath {
		t.Fatalf("echo asymmetry: sent %q, reported %v", clientPath, info[clientPath]["path"])
	}

	rec = do(t, s, "POST", "/command",
		strings.NewReader(`{"command":"cat input.txt","cwd":"/tmp/workspace/job"}`),
		map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Fatalf("command absolute cwd = %d %s", rec.Code, rec.Body.String())
	}
	var output string
	for _, ev := range parseSSE(t, rec.Body.String()) {
		if ev.Type == EventStdout {
			output += ev.Text
		}
	}
	if !strings.Contains(output, "bed-local") {
		t.Fatalf("command cwd and file API resolved different locations: %q", output)
	}
}

func TestCommandCwdCanBeBedRoot(t *testing.T) {
	s := newTestServer(t)
	rec := do(t, s, http.MethodPost, "/command",
		strings.NewReader(`{"command":"printf bed-root > marker.txt","cwd":"/"}`),
		map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Fatalf("command cwd=/ = %d %s", rec.Code, rec.Body.String())
	}
	events := parseSSE(t, rec.Body.String())
	if len(events) == 0 || events[len(events)-1].Result == nil ||
		events[len(events)-1].Result.Process.ExitCode == nil ||
		*events[len(events)-1].Result.Process.ExitCode != 0 {
		t.Fatalf("command cwd=/ events = %+v", events)
	}

	rec = do(t, s, http.MethodGet, "/files/download?path=/marker.txt", nil, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "bed-root" {
		t.Fatalf("bed root file = %d %q", rec.Code, rec.Body.String())
	}
}

func TestCommandCwdPreservesHeredocTerminatorAtEOF(t *testing.T) {
	s := newTestServer(t)
	body, err := json.Marshal(RunCommandRequest{
		Command: "cat <<'PY'\nheredoc-ok\nPY",
		Cwd:     "/workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, s, http.MethodPost, "/command", bytes.NewReader(body),
		map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Fatalf("command heredoc = %d %s", rec.Code, rec.Body.String())
	}

	var stdout, stderr string
	var exitCode *int
	for _, event := range parseSSE(t, rec.Body.String()) {
		switch event.Type {
		case EventStdout:
			stdout += event.Text
		case EventStderr:
			stderr += event.Text
		case EventExecutionEnd:
			if event.Result != nil {
				exitCode = event.Result.Process.ExitCode
			}
		}
	}
	if stdout != "heredoc-ok\n" || stderr != "" || exitCode == nil || *exitCode != 0 {
		t.Fatalf("command heredoc stdout=%q stderr=%q exit_code=%v", stdout, stderr, exitCode)
	}
}

// parseSSE extracts the JSON event frames from an SSE body.
func parseSSE(t *testing.T, body string) []StreamEvent {
	t.Helper()
	var evs []StreamEvent
	for _, frame := range strings.Split(body, "\n\n") {
		frame = strings.TrimSpace(frame)
		if frame == "" {
			continue
		}
		var ev StreamEvent
		if err := json.Unmarshal([]byte(frame), &ev); err != nil {
			t.Fatalf("bad SSE frame %q: %v", frame, err)
		}
		evs = append(evs, ev)
	}
	return evs
}

func TestCommandForegroundSSE(t *testing.T) {
	s := newTestServer(t)
	rec := do(t, s, "POST", "/command", strings.NewReader(`{"command":"echo hostel-ok"}`),
		map[string]string{"Content-Type": "application/json"})
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	evs := parseSSE(t, rec.Body.String())
	var sawStarted, sawPing, sawStdout, sawFinished bool
	for _, ev := range evs {
		if ev.Type == EventExecutionStart && ev.ExecutionID != "" {
			sawStarted = true
		}
		if ev.Type == EventPing && ev.Text == "pong" {
			sawPing = true
		}
		if ev.Type == EventStdout && strings.Contains(ev.Text, "hostel-ok") {
			sawStdout = true
		}
		if ev.Type == EventExecutionEnd {
			sawFinished = true
			if ev.Result == nil || ev.Result.Process.ExitCode == nil || *ev.Result.Process.ExitCode != 0 {
				t.Fatalf("finished result = %+v", ev.Result)
			}
		}
	}
	if !sawStarted || !sawPing || !sawStdout || !sawFinished {
		t.Fatalf("SSE missing events: started=%v ping=%v stdout=%v finished=%v (%+v)", sawStarted, sawPing, sawStdout, sawFinished, evs)
	}
}

func TestCommandSilentExecStartsStreamBeforeCompletion(t *testing.T) {
	s := newTestServer(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	client := ts.Client()
	client.Timeout = time.Second
	resp, err := client.Post(ts.URL+"/command", "application/json", strings.NewReader(`{"command":"sleep 2"}`))
	if err != nil {
		t.Fatalf("wait for command stream headers: %v", err)
	}
	defer resp.Body.Close()

	frame, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil {
		t.Fatalf("read first command event: %v", err)
	}
	var ev StreamEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(frame)), &ev); err != nil {
		t.Fatalf("decode first command event %q: %v", frame, err)
	}
	if ev.Type != EventExecutionStart || ev.ExecutionID == "" {
		t.Fatalf("first command event = %+v, want execution_start", ev)
	}
}

func TestSSEHeartbeatStopsWithHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/command", nil)

	stop := newSSE(c).start(c.Request.Context(), "exec-1", time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	stop()
	before := rec.Body.String()
	time.Sleep(3 * time.Millisecond)

	if rec.Body.String() != before {
		t.Fatal("heartbeat wrote after cleanup returned")
	}
	var pings int
	for _, ev := range parseSSE(t, before) {
		if ev.Type == EventPing {
			pings++
		}
	}
	if pings < 2 {
		t.Fatalf("heartbeat pings = %d, want at least 2", pings)
	}
}

func TestCommandForegroundPreservesStdoutAndStderr(t *testing.T) {
	s := newTestServer(t)
	rec := do(t, s, "POST", "/command", strings.NewReader(`{"command":"printf out; printf err >&2; exit 7"}`),
		map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Fatalf("command = %d %s", rec.Code, rec.Body.String())
	}
	var stdout, stderr string
	var exitCode *int
	var result *executionResultPayload
	for _, ev := range parseSSE(t, rec.Body.String()) {
		switch ev.Type {
		case EventStdout:
			stdout += ev.Text
		case EventStderr:
			stderr += ev.Text
		case EventExecutionEnd:
			if ev.Result != nil {
				result = ev.Result
				exitCode = ev.Result.Process.ExitCode
			}
		}
	}
	if stdout != "out" || stderr != "err" || exitCode == nil || *exitCode != 7 {
		t.Fatalf("typed command output: stdout=%q stderr=%q exit=%v", stdout, stderr, exitCode)
	}
	if result == nil || result.ExecutorID == "" || result.ExecutorBackend != "local" {
		t.Fatalf("execution executor identity = %+v", result)
	}
}

func TestLostExecutorResultDoesNotExposeTransportError(t *testing.T) {
	payload := resultPayload(bed.ExecutionResult{
		ExecutionID:     "exec-test",
		BedID:           "bed-test",
		ExecutorID:      "executor-test",
		ExecutorBackend: "supervisor",
		Process:         executor.Lost("executor-test", io.EOF),
		Cause:           bed.CauseExecutorLost,
	})
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "EOF") || payload.Process.Error != "executor executor-test lost" {
		t.Fatalf("public lost result = %s", encoded)
	}
}

func TestSessionStatePersistsAcrossRuns(t *testing.T) {
	s := newTestServer(t)
	rec := do(t, s, "POST", "/session", strings.NewReader(`{}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != 200 {
		t.Fatalf("create session = %d %s", rec.Code, rec.Body.String())
	}
	var cr struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &cr)
	if cr.SessionID == "" {
		t.Fatal("no session_id")
	}

	run := func(cmd string) []StreamEvent {
		rec := do(t, s, "POST", "/session/"+cr.SessionID+"/run",
			strings.NewReader(`{"command":`+jsonStr(cmd)+`}`),
			map[string]string{"Content-Type": "application/json"})
		if rec.Code != 200 {
			t.Fatalf("run %q = %d %s", cmd, rec.Code, rec.Body.String())
		}
		return parseSSE(t, rec.Body.String())
	}
	// Shell cwd starts at the bed workspace (host dir); use relative paths — the
	// /workspace virtual prefix is a file-API convenience, not a shell mount (v1).
	run("mkdir -p subdir && cd subdir")
	evs := run("pwd")
	joined := ""
	for _, ev := range evs {
		if ev.Type == EventStdout {
			joined += ev.Text
		}
	}
	if !strings.Contains(joined, "subdir") {
		t.Fatalf("cwd not preserved across runs: %q", joined)
	}
}

func TestBedIsolationAcrossHeader(t *testing.T) {
	s := newTestServer(t)
	// Write a file into bed "alice".
	up := func(bedID, path, content string) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("metadata", `{"path":"`+path+`"}`)
		fw, _ := mw.CreateFormFile("file", "f")
		_, _ = fw.Write([]byte(content))
		_ = mw.Close()
		rec := do(t, s, "POST", "/files/upload", &buf, map[string]string{
			"Content-Type": mw.FormDataContentType(),
			BedHeader:      bedID,
		})
		if rec.Code != 200 {
			t.Fatalf("upload bed=%s = %d %s", bedID, rec.Code, rec.Body.String())
		}
	}
	up("alice", "/tmp/workspace/secret.txt", "alice-data")

	// bob's bed must NOT see alice's file.
	rec := do(t, s, "GET", "/files/download?path=/tmp/workspace/secret.txt", nil, map[string]string{BedHeader: "bob"})
	if rec.Code != 404 {
		t.Fatalf("bob reading alice's file = %d (want 404)", rec.Code)
	}
	// alice still sees her own.
	rec = do(t, s, "GET", "/files/download?path=/tmp/workspace/secret.txt", nil, map[string]string{BedHeader: "alice"})
	if rec.Code != 200 || rec.Body.String() != "alice-data" {
		t.Fatalf("alice reading own file = %d %q", rec.Code, rec.Body.String())
	}
}

func TestBedsCRUD(t *testing.T) {
	s := newTestServer(t)
	rec := createBedAndWait(t, s, "conv-1")
	rec = do(t, s, "GET", "/v1/beds", nil, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "conv-1") {
		t.Fatalf("list beds = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s, "GET", "/v1/beds/conv-1", nil, nil)
	var detail struct {
		Generation int64 `json:"generation"`
		Activity   struct {
			Operations map[string]int `json:"operations"`
			Sessions   map[string]int `json:"sessions"`
		} `json:"activity"`
		Lifecycle *struct {
			LastInitialization *struct {
				Action string `json:"action"`
				Result string `json:"result"`
				Source string `json:"source"`
				Stages []struct {
					Name string `json:"name"`
				} `json:"stages"`
			} `json:"last_initialization"`
			LastPersist *struct {
				Trigger string `json:"trigger"`
			} `json:"last_persist"`
		} `json:"lifecycle"`
		Executor *struct {
			ID      string `json:"id"`
			Backend string `json:"backend"`
			State   string `json:"state"`
		} `json:"executor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode bed detail: %v", err)
	}
	if detail.Lifecycle == nil || detail.Lifecycle.LastInitialization == nil ||
		detail.Lifecycle.LastInitialization.Action != "initialize" ||
		detail.Lifecycle.LastInitialization.Result != "success" ||
		detail.Lifecycle.LastInitialization.Source != "fresh" ||
		len(detail.Lifecycle.LastInitialization.Stages) == 0 {
		t.Fatalf("bed detail initialization = %+v", detail.Lifecycle)
	}
	if detail.Generation != 0 || len(detail.Activity.Operations) != 0 {
		t.Fatalf("bed detail current state = generation %d activity %+v", detail.Generation, detail.Activity)
	}
	if detail.Executor != nil {
		t.Fatalf("unused bed unexpectedly has executor %+v", detail.Executor)
	}

	rec = do(t, s, "POST", "/v1/beds/conv-1/checkpoint", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("checkpoint bed = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s, "GET", "/v1/beds/conv-1", nil, nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode checkpointed bed detail: %v", err)
	}
	if detail.Generation != 1 || len(detail.Activity.Operations) != 0 ||
		detail.Lifecycle == nil || detail.Lifecycle.LastPersist == nil ||
		detail.Lifecycle.LastPersist.Trigger != "checkpoint" {
		t.Fatalf("checkpointed bed detail = %+v", detail)
	}

	rec = do(t, s, "DELETE", "/v1/beds/conv-1", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("delete bed = %d", rec.Code)
	}
	rec = do(t, s, "GET", "/v1/beds/conv-1", nil, nil)
	if rec.Code != 404 {
		t.Fatalf("get deleted bed = %d (want 404)", rec.Code)
	}
}

func TestCapabilities(t *testing.T) {
	s := newTestServer(t)
	rec := do(t, s, "GET", "/v1/beds/capabilities", nil, nil)
	var caps map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &caps)
	accounting, _ := caps["resource_accounting"].(map[string]any)
	if caps["command"] != true || caps["pty"] != false ||
		accounting == nil || accounting["backend"] != "noop" {
		t.Fatalf("capabilities = %v", caps)
	}
}

func TestCommandRejectsReservedEnvNamespace(t *testing.T) {
	s := newTestServer(t)
	rec := do(t, s, http.MethodPost, "/command",
		strings.NewReader(`{"command":"true","envs":{"BED_ID":"spoof"}}`),
		map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "reserved namespace") {
		t.Fatalf("reserved env = %d %s", rec.Code, rec.Body.String())
	}
}

func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }

var _ = http.StatusOK

func TestMaxBedsBackpressure(t *testing.T) {
	root := t.TempDir()
	mgr, err := bed.NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 1, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	s := NewServer(mgr)

	// First bed fills the only slot.
	rec := do(t, s, "POST", "/v1/beds", strings.NewReader(`{"id":"one"}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create one = %d %s", rec.Code, rec.Body.String())
	}
	// Second bed → 429 BED_LIMIT_EXCEEDED, whether via management API...
	rec = do(t, s, "POST", "/v1/beds", strings.NewReader(`{"id":"two"}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "BED_LIMIT_EXCEEDED") {
		t.Fatalf("create two = %d %s (want 429 BED_LIMIT_EXCEEDED)", rec.Code, rec.Body.String())
	}
	// ...or via implicit creation on any endpoint.
	rec = do(t, s, "GET", "/files/info?path=/workspace", nil, map[string]string{BedHeader: "three"})
	if rec.Code != 429 {
		t.Fatalf("implicit create three = %d (want 429)", rec.Code)
	}
	// Default bed still works on a full instance.
	rec = do(t, s, "GET", "/files/info?path=/workspace", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("default bed on full instance = %d %s", rec.Code, rec.Body.String())
	}
	// Capacity is reported for scheduler placement.
	rec = do(t, s, "GET", "/healthz", nil, nil)
	var h map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &h)
	if h["max_beds"] != float64(1) {
		t.Fatalf("healthz max_beds = %v, want 1", h["max_beds"])
	}
	waitBedReady(t, s, "one")
}

func TestMaxPinnedBedsBackpressure(t *testing.T) {
	root := t.TempDir()
	mgr, err := bed.NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 3, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := mgr.SetMaxPinnedBeds(1); err != nil {
		t.Fatalf("SetMaxPinnedBeds: %v", err)
	}
	s := NewServer(mgr)
	one, _ := mgr.Ensure(context.Background(), "one")
	_, _ = mgr.Ensure(context.Background(), "two")
	finish, err := mgr.BeginOperation(one, bed.OpExec, time.Minute)
	if err != nil {
		t.Fatalf("initialize one: %v", err)
	}
	// Capacity pressure never rejects a resident old guest.
	rec := do(t, s, http.MethodGet, "/files/info?path=/workspace", nil, map[string]string{BedHeader: "one"})
	if rec.Code != http.StatusOK || mgr.PinnedBedCount() != 1 {
		t.Fatalf("request on pinned bed = %d pinned=%d body=%s", rec.Code, mgr.PinnedBedCount(), rec.Body.String())
	}
	// A synced idle bed no longer owns this carrier and is rejected while it is
	// pressured, allowing the upstream scheduler to choose another carrier.
	rec = do(t, s, http.MethodGet, "/files/info?path=/workspace", nil, map[string]string{BedHeader: "two"})
	var pressure ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &pressure); err != nil {
		t.Fatalf("decode pressure response: %v", err)
	}
	if rec.Code != http.StatusTooManyRequests || pressure.Code != ErrInsufficientBed ||
		!pressure.Retryable || pressure.Pressure == nil ||
		pressure.Pressure.PinnedBeds != 1 || pressure.Pressure.MaxPinnedBeds != 1 ||
		pressure.Pressure.ResidentBeds != 2 || pressure.Pressure.MaxBeds != 3 {
		t.Fatalf("admit two = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s, http.MethodPost, "/v1/beds/two/checkpoint", nil, nil)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "INSUFFICIENT_BED") {
		t.Fatalf("checkpoint two = %d %s", rec.Code, rec.Body.String())
	}
	// The default bed bypasses both limits and does not affect the pinned count.
	rec = do(t, s, http.MethodGet, "/files/info?path=/workspace", nil, nil)
	if rec.Code != http.StatusOK || mgr.PinnedBedCount() != 1 {
		t.Fatalf("default during pinned cap = %d pinned=%d body=%s", rec.Code, mgr.PinnedBedCount(), rec.Body.String())
	}

	rec = do(t, s, http.MethodGet, "/healthz", nil, nil)
	var health map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &health)
	if health["pinned_beds"] != float64(1) || health["max_pinned_beds"] != float64(1) || health["bed_pressure"] != true {
		t.Fatalf("health pinned capacity = %v", health)
	}
	rec = do(t, s, http.MethodGet, "/v1/beds", nil, nil)
	var inventory struct {
		Instance struct {
			BedPressure bool `json:"bed_pressure"`
		} `json:"instance"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &inventory); err != nil || !inventory.Instance.BedPressure {
		t.Fatalf("inventory bed pressure = %+v err=%v", inventory.Instance, err)
	}

	finish()
	rec = do(t, s, http.MethodGet, "/files/info?path=/workspace", nil, map[string]string{BedHeader: "two"})
	if rec.Code != http.StatusOK {
		t.Fatalf("admit two after release = %d %s", rec.Code, rec.Body.String())
	}
}

func TestCheckpointEndpointAndPersistenceReporting(t *testing.T) {
	root := t.TempDir()
	mgr, err := bed.NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	s := NewServer(mgr)

	// Checkpoint an existing bed (noop backend → trivially succeeds).
	rec := createBedAndWait(t, s, "cp")
	rec = do(t, s, "POST", "/v1/beds/cp/checkpoint", nil, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"persistence":"noop"`) {
		t.Fatalf("checkpoint = %d %s", rec.Code, rec.Body.String())
	}
	// Unknown bed → runtime error, not a crash.
	rec = do(t, s, "POST", "/v1/beds/ghost/checkpoint", nil, nil)
	if rec.Code != 500 {
		t.Fatalf("checkpoint unknown bed = %d", rec.Code)
	}

	// healthz reports the backend.
	rec = do(t, s, "GET", "/healthz", nil, nil)
	if !strings.Contains(rec.Body.String(), `"persistence":"noop"`) {
		t.Fatalf("healthz missing persistence: %s", rec.Body.String())
	}
}

// /v1/beds is the scheduler's one-poll picture: instance capacity plus
// every local bed — in-memory ones and luggage (evicted, dir kept).
func TestBedListDefaultBedRetainsInstanceWithoutInventoryRow(t *testing.T) {
	s := newTestServer(t)
	if _, err := s.mgr.Ensure(context.Background(), ""); err != nil {
		t.Fatalf("ensure default bed: %v", err)
	}

	rec := do(t, s, http.MethodGet, "/v1/beds", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("bed list = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Instance struct {
			Status string `json:"status"`
		} `json:"instance"`
		Beds []bedView `json:"beds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Instance.Status != "retained" {
		t.Fatalf("instance status = %s, want retained while default bed is resident", body.Instance.Status)
	}
	if len(body.Beds) != 0 {
		t.Fatalf("scheduler inventory contains default bed: %+v", body.Beds)
	}
}

func TestBedListEndpoint(t *testing.T) {
	s := newTestServer(t)
	s.mgr.SetBedIdleTTL(time.Minute)
	s.mgr.SetLuggageLimits(1000, 800)

	rec := createBedAndWait(t, s, "inv-live")
	rec = createBedAndWait(t, s, "inv-idle")
	rec = createBedAndWait(t, s, "inv-cold")
	if rec = do(t, s, "DELETE", "/v1/beds/inv-cold", nil, nil); rec.Code != 200 {
		t.Fatalf("evict cold = %d", rec.Code)
	}
	live, ok := s.mgr.Get("inv-live")
	if !ok {
		t.Fatal("inv-live bed missing")
	}
	finishOperation, err := s.mgr.BeginOperation(live, bed.OpControl, time.Minute)
	if err != nil {
		t.Fatalf("BeginOperation: %v", err)
	}
	defer finishOperation()

	rec = do(t, s, "GET", "/v1/beds", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("bed list = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Instance struct {
			Status           string         `json:"status"`
			Store            string         `json:"store"`
			MaxBeds          int            `json:"max_beds"`
			PinnedBeds       int            `json:"pinned_beds"`
			MaxPinnedBeds    int            `json:"max_pinned_beds"`
			PhaseCounts      map[string]int `json:"phase_counts"`
			ActivityCounts   map[string]int `json:"activity_counts"`
			RetainUntil      time.Time      `json:"retained_until"`
			LuggageHighBytes int64          `json:"luggage_high_bytes"`
		} `json:"instance"`
		Beds []struct {
			ID     string `json:"id"`
			Status struct {
				Phase    string `json:"phase"`
				Activity string `json:"activity"`
			} `json:"status"`
			Pinned       bool      `json:"pinned"`
			LastActiveAt time.Time `json:"last_active_at"`
			RetainUntil  time.Time `json:"retained_until"`
		} `json:"beds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Instance.Status != "retained" {
		t.Fatalf("instance status = %s, want retained (a bed is within its retention promise)", body.Instance.Status)
	}
	if body.Instance.Store != "noop" || body.Instance.PinnedBeds != 1 ||
		body.Instance.ActivityCounts["active"] != 1 || body.Instance.ActivityCounts["idle"] != 1 ||
		body.Instance.PhaseCounts["resident"] != 2 || body.Instance.PhaseCounts["evicting"] != 0 ||
		body.Instance.PhaseCounts["purging"] != 0 || body.Instance.PhaseCounts["dormant"] != 1 ||
		body.Instance.RetainUntil.IsZero() ||
		body.Instance.LuggageHighBytes != 1000 {
		t.Fatalf("instance = %+v", body.Instance)
	}
	if want := live.RetainUntil(); !body.Instance.RetainUntil.Equal(want) {
		t.Fatalf("instance retained_until = %s, want max bed retention %s", body.Instance.RetainUntil, want)
	}
	statuses := map[string]string{}
	for _, b := range body.Beds {
		statuses[b.ID] = b.Status.Phase + "/" + b.Status.Activity
		if b.ID == "inv-live" && (b.LastActiveAt.IsZero() || b.RetainUntil.IsZero()) {
			t.Fatalf("inv-live lifecycle fields = %+v", b)
		}
		if b.ID == "inv-live" && !b.Pinned {
			t.Fatalf("active inventory bed should be pinned: %+v", b)
		}
	}
	if statuses["inv-live"] != "resident/active" || statuses["inv-idle"] != "resident/idle" || statuses["inv-cold"] != "dormant/" {
		t.Fatalf("bed statuses = %v, want resident/active, resident/idle, dormant", statuses)
	}

	// Evict every resident bed under the noop store: the local luggage is the
	// only copy, so the instance reports pinned.
	finishOperation()
	for _, id := range []string{"inv-live", "inv-idle"} {
		if rec = do(t, s, "DELETE", "/v1/beds/"+id, nil, nil); rec.Code != 200 {
			t.Fatalf("evict %s = %d", id, rec.Code)
		}
	}
	rec = do(t, s, "GET", "/v1/beds", nil, nil)
	var pinned struct {
		Instance struct {
			Status string `json:"status"`
		} `json:"instance"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pinned); err != nil {
		t.Fatalf("unmarshal pinned: %v", err)
	}
	if pinned.Instance.Status != "pinned" {
		t.Fatalf("instance status after evicting all = %s, want pinned (noop store, local copy only)", pinned.Instance.Status)
	}
}

func TestDeleteEvictVsPurge(t *testing.T) {
	s := newTestServer(t)
	// Create, then default DELETE = evict (noop store: no snapshot, but 200).
	rec := createBedAndWait(t, s, "lifecycle")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"activity":"idle"`) {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s, "DELETE", "/v1/beds/lifecycle", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("evict = %d %s", rec.Code, rec.Body.String())
	}
	// Purge an absent bed is idempotent (snapshot delete of missing key is OK).
	rec = do(t, s, "DELETE", "/v1/beds/lifecycle?purge=true", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("purge = %d %s", rec.Code, rec.Body.String())
	}
	// Default bed refuses purge.
	rec = do(t, s, "DELETE", "/v1/beds/default?purge=true", nil, nil)
	if rec.Code != 400 {
		t.Fatalf("purge default = %d (want 400 client error)", rec.Code)
	}
}
