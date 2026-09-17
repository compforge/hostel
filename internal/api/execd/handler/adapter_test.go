package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	bed "github.com/qiankunli/hostel/internal/bed/manager"
)

func TestAdapterFileContract(t *testing.T) {
	s := newTestServer(t)
	createBedAndWait(t, s, "files")
	for _, base := range []string{"/execd", "/v1/beds/files/execd"} {
		t.Run(base, func(t *testing.T) {
			hdr := map[string]string{BedHeader: "files"}
			request := func(method, path, body string) *httptest.ResponseRecorder {
				return do(t, s, method, base+path, strings.NewReader(body), hdr)
			}
			must := func(rec *httptest.ResponseRecorder, want int) {
				t.Helper()
				if rec.Code != want {
					t.Fatalf("status=%d want=%d: %s", rec.Code, want, rec.Body.String())
				}
			}
			must(request("POST", "/directories", `{"/tmp/contract/sub":{"mode":755}}`), 200)
			b, _ := s.mgr.Get("files")
			if err := b.BedFS().Write("/tmp/contract/a", []byte("one\ntwo\nthree\n"), 0600); err != nil {
				t.Fatal(err)
			}
			rec := request("GET", "/files/info?path=/tmp/contract/a", "")
			must(rec, 200)
			var info map[string]map[string]any
			json.Unmarshal(rec.Body.Bytes(), &info)
			if info["/tmp/contract/a"]["mode"] != float64(600) || info["/tmp/contract/a"]["created_at"] == nil || info["/tmp/contract/a"]["owner"] == "" {
				t.Fatal(rec.Body.String())
			}
			must(request("POST", "/files/permissions", `{"/tmp/contract/a":{"mode":640}}`), 200)
			fi, _ := b.BedFS().Stat("/tmp/contract/a")
			if fi.Mode != 0640 {
				t.Fatal(fi)
			}
			must(request("POST", "/files/permissions", `{"/tmp/contract/a":{"mode":888}}`), 400)
			must(request("POST", "/files/permissions", `{"/tmp/contract/a":{"mode":644,"owner":"nonexistent-privileged-user"}}`), 400)
			rec = request("GET", "/files/download?path=/tmp/contract/a&offset=2&limit=1", "")
			must(rec, 200)
			if rec.Body.String() != "two\n" {
				t.Fatal(rec.Body.String())
			}
			for _, tc := range []struct {
				span, want string
				status     int
			}{{"bytes=4-6", "two", 206}, {"bytes=-6", "three\n", 206}, {"bytes=999-1000", "", 416}} {
				headers := map[string]string{BedHeader: "files", "Range": tc.span}
				rec = do(t, s, "GET", base+"/files/download?path=/tmp/contract/a", nil, headers)
				must(rec, tc.status)
				if tc.status == 206 && (rec.Body.String() != tc.want || rec.Header().Get("Content-Range") == "") {
					t.Fatal(rec.Body.String(), rec.Header())
				}
			}
			must(do(t, s, "GET", base+"/files/download?path=/tmp/contract/a&offset=1", nil, map[string]string{BedHeader: "files", "Range": "bytes=0-1"}), 400)
			for _, q := range []string{"offset=0", "offset=-1", "limit=0", "limit=nope"} {
				must(request("GET", "/files/download?path=/tmp/contract/a&"+q, ""), 400)
			}
			rec = request("GET", "/directories/list?path=/tmp/contract&depth=0", "")
			must(rec, 200)
			if strings.TrimSpace(rec.Body.String()) != "[]" {
				t.Fatal(rec.Body.String())
			}
			must(request("GET", "/directories/list?path=/tmp/contract&depth=-1", ""), 400)
			rec = request("POST", "/files/replace", `{"/tmp/contract/a":{"old":"two","new":"TWO"}}`)
			must(rec, 200)
			if !strings.Contains(rec.Body.String(), `"replacedCount":1`) {
				t.Fatal(rec.Body.String())
			}
			must(request("POST", "/files/mv", `[{"src":"/tmp/contract/a","dest":"/tmp/contract/b"}]`), 200)
			rec = request("GET", "/files/search?path=/tmp/contract&pattern=b", "")
			must(rec, 200)
			if !strings.Contains(rec.Body.String(), "/tmp/contract/b") {
				t.Fatal(rec.Body.String())
			}
			must(request("DELETE", "/files?path=/tmp/contract/b", ""), 200)
			must(request("GET", "/files/info?path=/tmp/contract/b", ""), 404)
			must(request("DELETE", "/directories?path=/tmp/contract", ""), 200)
		})
	}
}
func TestAdapterSessionAndBackground(t *testing.T) {
	s := newTestServer(t)
	createBedAndWait(t, s, "a")
	createBedAndWait(t, s, "b")
	for _, base := range []string{"/execd", "/v1/beds/a/execd"} {
		t.Run(base, func(t *testing.T) {
			hdr := map[string]string{BedHeader: "a"}
			request := func(method, path, body string) *httptest.ResponseRecorder {
				return do(t, s, method, base+path, strings.NewReader(body), hdr)
			}
			rec := request("POST", "/session", "{}")
			if rec.Code != 200 {
				t.Fatal(rec.Body.String())
			}
			var session struct {
				ID string `json:"session_id"`
			}
			json.Unmarshal(rec.Body.Bytes(), &session)
			run := func(command, cwd string) *httptest.ResponseRecorder {
				raw, _ := json.Marshal(map[string]any{"command": command, "cwd": cwd, "timeout": 2000})
				return request("POST", "/session/"+session.ID+"/run", string(raw))
			}
			rec = run(`export NEXT="$HOME/sub"; mkdir -p "$NEXT"; export KEPT=yes`, "")
			if !strings.Contains(rec.Body.String(), "execution_complete") {
				t.Fatal(rec.Body.String())
			}
			rec = run(`printf '%s:%s' "$KEPT" "${PWD##*/}"`, "$NEXT")
			if !strings.Contains(rec.Body.String(), "yes:sub") {
				t.Fatal(rec.Body.String())
			}
			rec = run("pwd", "$(touch /tmp/should-not-run)")
			if !strings.Contains(rec.Body.String(), `"type":"error"`) {
				t.Fatal(rec.Body.String())
			}
			rec = run("printf alive", "")
			if !strings.Contains(rec.Body.String(), "alive") {
				t.Fatal(rec.Body.String())
			}
			wrong := do(t, s, "POST", "/v1/beds/b/execd/session/"+session.ID+"/run", strings.NewReader(`{"command":"true"}`), nil)
			if wrong.Code != 404 {
				t.Fatal(wrong.Code)
			}
			if rec = request("DELETE", "/session/"+session.ID, ""); rec.Code != 200 {
				t.Fatal(rec.Body.String())
			}
			if rec = run("true", ""); rec.Code != 404 {
				t.Fatal(rec.Code)
			}
			rec = request("POST", "/command", `{"command":"printf partial; sleep 30","background":true}`)
			events := decodeExecd(t, rec.Body.String())
			if len(events) != 2 || events[0].Type != "init" || events[1].Type != "execution_complete" {
				t.Fatal(rec.Body.String())
			}
			id := events[0].Text
			deadline := time.Now().Add(2 * time.Second)
			for {
				rec = request("GET", "/command/"+id+"/logs", "")
				if bytes.Equal(rec.Body.Bytes(), []byte("partial")) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal(rec.Body.String())
				}
				time.Sleep(time.Millisecond)
			}
			if rec.Header().Get("EXECD-COMMANDS-TAIL-CURSOR") != "7" {
				t.Fatal(rec.Header())
			}
			rec = request("GET", "/command/"+id+"/logs?cursor=7", "")
			if rec.Body.Len() != 0 {
				t.Fatal(rec.Body.String())
			}
			for _, path := range []string{"/command/status/" + id, "/command/" + id + "/logs"} {
				wrong = do(t, s, "GET", "/v1/beds/b/execd"+path, nil, nil)
				if wrong.Code != 404 {
					t.Fatal(wrong.Code)
				}
			}
			rec = request("DELETE", "/command?id="+id, "")
			if rec.Code != 200 {
				t.Fatal(rec.Body.String())
			}
			e, _ := s.mgr.Executions().Get(id)
			e.Wait()
			rec = request("GET", "/command/status/"+id, "")
			if !strings.Contains(rec.Body.String(), `"running":false`) || !strings.Contains(rec.Body.String(), `"exit_code":`) {
				t.Fatal(rec.Body.String())
			}
			for _, path := range []string{"/code", "/code/context", "/code/contexts", "/code/contexts/ctx"} {
				rec = request("POST", path, `{}`)
				if rec.Code != 501 {
					t.Fatal(path, rec.Code)
				}
			}
		})
	}
}
func TestAdapterLargeBackgroundOutput(t *testing.T) {
	s := newTestServer(t)
	createBedAndWait(t, s, "large")
	raw, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("printf '%%0100000d' 0"), "background": true})
	rec := do(t, s, "POST", "/v1/beds/large/execd/command", bytes.NewReader(raw), nil)
	events := decodeExecd(t, rec.Body.String())
	if len(events) == 0 {
		t.Fatal(rec.Body.String())
	}
	e, _ := s.mgr.Executions().Get(events[0].Text)
	e.Wait()
	rec = do(t, s, "GET", "/v1/beds/large/execd/command/"+e.ID+"/logs", nil, nil)
	if rec.Code != 200 || rec.Body.Len() != 100000 {
		t.Fatal(rec.Code, rec.Body.Len())
	}
}

func TestAdapterCannotInterruptService(t *testing.T) {
	s := newTestServer(t)
	createBedAndWait(t, s, "service")
	b, _ := s.mgr.Get("service")
	e, err := s.mgr.StartExecution(t.Context(), b, bed.ExecutionService, "sleep 30", "", "", nil, 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { e.RequestStop(bed.CauseInterrupted); e.Wait() }()
	for _, base := range []string{"/execd", "/v1/beds/service/execd"} {
		for _, request := range []struct{ method, path string }{
			{"DELETE", "/command?id=" + e.ID},
			{"GET", "/command/status/" + e.ID},
			{"GET", "/command/" + e.ID + "/logs"},
		} {
			rec := do(t, s, request.method, base+request.path, nil, map[string]string{BedHeader: "service"})
			if rec.Code != 404 {
				t.Fatalf("%s %s: %d %s", request.method, request.path, rec.Code, rec.Body.String())
			}
		}
	}
	if !e.Status().Running {
		t.Fatal("command adapter stopped a managed service")
	}
}
