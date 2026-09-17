package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/qiankunli/hostel/internal/api/execd/view"
)

func decodeExecd(t *testing.T, body string) []view.StreamEvent {
	t.Helper()
	var events []view.StreamEvent
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev view.StreamEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}
	return events
}

func TestExecdAddressesShareProtocolAndBed(t *testing.T) {
	s := newTestServer(t)
	createBedAndWait(t, s, "bed-a")
	createBedAndWait(t, s, "bed-b")
	for _, base := range []string{"/execd", "/v1/beds/bed-a/execd"} {
		t.Run(base, func(t *testing.T) {
			hdr := map[string]string{BedHeader: "bed-a", "Content-Type": "application/json"}
			if strings.HasPrefix(base, "/v1/") {
				hdr[BedHeader] = "bed-b"
			} // Path must win.
			for _, tc := range []struct {
				command, value string
				timeout        int
			}{{"printf '实际输出'; printf '错误输出' >&2; exit 7", "7", 3000}, {"printf ok", "", 3000}, {"sleep 3", "124", 20}} {
				raw, _ := json.Marshal(map[string]any{"command": tc.command, "cwd": "/workspace", "timeout": tc.timeout})
				rec := do(t, s, "POST", base+"/command", bytes.NewReader(raw), hdr)
				if rec.Code != 200 {
					t.Fatal(rec.Code, rec.Body.String())
				}
				if tc.value == "7" && (!strings.Contains(rec.Body.String(), `"ename":"CommandExecError"`) || !strings.Contains(rec.Body.String(), `"evalue":"7"`)) {
					t.Fatal("OpenSandbox error keys changed", rec.Body.String())
				}
				events := decodeExecd(t, rec.Body.String())
				if len(events) < 2 || events[0].Type != "init" {
					t.Fatal(events)
				}
				execution, ok := s.mgr.Executions().Get(events[0].Text)
				a, _ := s.mgr.Get("bed-a")
				if !ok || execution.BedID != a.Name {
					t.Fatal("wrong bed execution")
				}
				var terminal *view.StreamEvent
				var stdout, stderr string
				for i, ev := range events {
					switch ev.Type {
					case "error", "execution_complete":
						if terminal != nil {
							t.Fatal("multiple terminal events")
						}
						terminal = &events[i]
					case "stdout":
						stdout += ev.Text
					case "stderr":
						stderr += ev.Text
					case "execution_end", "execution_start":
						t.Fatal("native event leaked")
					}
				}
				if terminal == nil {
					t.Fatal("missing terminal event")
				}
				if tc.value == "" {
					if terminal.Type != "execution_complete" || stdout != "ok" {
						t.Fatal(events)
					}
				} else if terminal.Type != "error" || terminal.Error == nil || terminal.Error.Name != "CommandExecError" || terminal.Error.Value != tc.value {
					t.Fatal(events)
				}
				if tc.value == "7" && (stdout != "实际输出" || stderr != "错误输出") {
					t.Fatal("lost output", stdout, stderr)
				}
				wrong := do(t, s, "DELETE", "/v1/beds/bed-b/execd/command?id="+url.QueryEscape(events[0].Text), nil, nil)
				if wrong.Code != 404 {
					t.Fatal("cross-bed cancellation accepted", wrong.Code)
				}
			}
		})
	}
	// Native stream remains unchanged.
	native := do(t, s, "POST", "/command", strings.NewReader(`{"command":"true"}`), map[string]string{BedHeader: "bed-a"})
	if !strings.Contains(native.Body.String(), `"type":"execution_end"`) || strings.Contains(native.Body.String(), `"type":"execution_complete"`) {
		t.Fatal(native.Body.String())
	}
	if err := s.mgr.Purge(context.Background(), "bed-a"); err != nil {
		t.Fatal(err)
	}
	rec := do(t, s, "POST", "/v1/beds/bed-a/execd/command", strings.NewReader(`{"command":"true"}`), nil)
	if rec.Code != 404 {
		t.Fatal("deleted bed recreated", rec.Code)
	}
	if _, ok := s.mgr.Get("bed-a"); ok {
		t.Fatal("missing bed created")
	}
}

func TestExecdFilesUseResolvedBed(t *testing.T) {
	s := newTestServer(t)
	createBedAndWait(t, s, "a")
	createBedAndWait(t, s, "b")
	content := []byte("附件\x00\xff\r\n")
	for _, base := range []string{"/execd", "/v1/beds/a/execd"} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		md, _ := writer.CreateFormFile("metadata", "metadata.json")
		_, _ = md.Write([]byte(`{"path":"/tmp/中文 '/file.bin","mode":600}`))
		file, _ := writer.CreateFormFile("file", "file.bin")
		_, _ = file.Write(content)
		writer.Close()
		hdr := map[string]string{BedHeader: "a", "Content-Type": writer.FormDataContentType()}
		rec := do(t, s, "POST", base+"/files/upload", &body, hdr)
		if rec.Code != 200 {
			t.Fatal(rec.Code, rec.Body.String())
		}
		a, _ := s.mgr.Get("a")
		info, err := a.BedFS().Stat("/tmp/中文 '/file.bin")
		if err != nil || info.Mode != 0600 {
			t.Fatal("upload permission metadata ignored", err, info)
		}
		suffix := "/files/download?" + url.Values{"path": {"/tmp/中文 '/file.bin"}}.Encode()
		rec = do(t, s, "GET", base+suffix, nil, hdr)
		if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), content) {
			t.Fatal(rec.Code, rec.Body.String())
		}
		other := do(t, s, "GET", "/v1/beds/b/execd"+suffix, nil, nil)
		if other.Code != 404 {
			t.Fatal("cross-bed file leak", other.Code)
		}
		for range 2 {
			rec = do(t, s, "DELETE", base+"/directories?"+url.Values{"path": {"/tmp/中文 '", "/tmp/absent"}}.Encode(), nil, hdr)
			if rec.Code != 200 {
				t.Fatal(rec.Code, rec.Body.String())
			}
		}
	}
}

func TestExecdLocalOperationsKeepAuthentication(t *testing.T) {
	s := newTestServer(t)
	var callbacks atomic.Int32
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callbacks.Add(1)
		http.Error(w, "unexpected control-plane callback", http.StatusServiceUnavailable)
	}))
	defer control.Close()
	// Persisted Bed declarations may retain old environment keys. They must not
	// turn local Execd operations into control-plane requests.
	raw, err := json.Marshal(map[string]any{"id": "secured", "env": map[string]string{
		"EXECD_ACCESS_TOKEN": "own-token", "EXECD_CONTROL_URL": control.URL,
	}})
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, s, http.MethodPost, "/v1/beds", bytes.NewReader(raw), nil)
	if rec.Code != 202 && rec.Code != 201 && rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if _, err := s.mgr.Ensure(context.Background(), "secured"); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"/execd", "/v1/beds/secured/execd"} {
		for _, token := range []string{"", "other-token", "own-token"} {
			hdr := map[string]string{BedHeader: "secured", "X-EXECD-ACCESS-TOKEN": token}
			rec = do(t, s, "POST", base+"/command", strings.NewReader(`{"command":"true"}`), hdr)
			want := 401
			if token == "own-token" {
				want = 200
			}
			if rec.Code != want {
				t.Fatal(rec.Code, want, rec.Body.String())
			}
			if token == "own-token" && !strings.Contains(rec.Body.String(), `"type":"execution_complete"`) {
				t.Fatal("command did not complete", rec.Body.String())
			}
		}
		hdr := map[string]string{BedHeader: "secured", "Authorization": "Bearer own-token"}
		rec = do(t, s, "POST", base+"/directories", strings.NewReader(`{"/workspace/local":{"mode":755}}`), hdr)
		if rec.Code != 200 {
			t.Fatal("local file operation failed", rec.Code, rec.Body.String())
		}
		rec = do(t, s, "POST", base+"/session", strings.NewReader(`{}`), hdr)
		if rec.Code != 200 {
			t.Fatal("local session creation failed", rec.Code, rec.Body.String())
		}
		var session struct {
			ID string `json:"session_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil || session.ID == "" {
			t.Fatal("invalid session response", err, rec.Body.String())
		}
		rec = do(t, s, "POST", base+"/session/"+session.ID+"/run", strings.NewReader(`{"command":"printf local","timeout":3000}`), hdr)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"text":"local"`) || !strings.Contains(rec.Body.String(), `"type":"execution_complete"`) {
			t.Fatal("local session execution failed", rec.Code, rec.Body.String())
		}
		rec = do(t, s, "DELETE", base+"/session/"+session.ID, nil, hdr)
		if rec.Code != 200 {
			t.Fatal("local session deletion failed", rec.Code, rec.Body.String())
		}
	}
	if got := callbacks.Load(); got != 0 {
		t.Fatalf("Execd called the control plane %d times", got)
	}
}
