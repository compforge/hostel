//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOpenSandboxExecd(t *testing.T) {
	target := startTarget(t, targetOptions{isolation: "dorm"})
	c := target.client
	must2xx(t, "create Execd bed", c.createBed(t, "execd-a"))
	must2xx(t, "create other bed", c.createBed(t, "execd-b"))
	for _, id := range []string{"execd-a", "execd-b"} {
		c.waitBed(t, id, func(b bedView) bool { return b.Status.Readiness.Ready }, "ready")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for _, base := range []string{"/execd", "/v1/beds/execd-a/execd"} {
		headerBed := "execd-a"
		if strings.HasPrefix(base, "/v1/") {
			headerBed = "execd-b"
		}
		response, err := c.json(ctx, "POST", base+"/command", headerBed, map[string]any{"command": "printf '真实输出'; printf '真实错误' >&2; exit 7", "cwd": "/workspace", "timeout": 3000}, nil)
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "direct command", response)
		var stdout, stderr string
		terminal := false
		init := false
		for _, line := range strings.Split(string(response.Body), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var ev struct {
				Type  string `json:"type"`
				Text  string `json:"text"`
				Error *struct {
					Name  string `json:"ename"`
					Value string `json:"evalue"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
				t.Fatal(err)
			}
			switch ev.Type {
			case "init":
				init = ev.Text != ""
			case "stdout":
				stdout += ev.Text
			case "stderr":
				stderr += ev.Text
			case "error":
				terminal = ev.Error != nil && ev.Error.Name == "CommandExecError" && ev.Error.Value == "7"
			case "execution_complete", "execution_end":
				t.Fatalf("false success/native event: %s", line)
			}
		}
		if !init || !terminal || stdout != "真实输出" || stderr != "真实错误" {
			t.Fatalf("wrong stream: %s", response.Body)
		}
		// Native upload and compatibility download share the same BedFS.
		must2xx(t, "prepare file", c.upload(t, "execd-a", "/tmp/execd/file.bin", []byte("真实文件\x00\xff")))
		suffix := "/files/download?path=" + url.QueryEscape("/tmp/execd/file.bin")
		file, err := c.request(ctx, "GET", base+suffix, headerBed, nil, "")
		if err != nil || file.Status != 200 || string(file.Body) != "真实文件\x00\xff" {
			t.Fatalf("download: %+v %v", file, err)
		}
		other, err := c.request(ctx, "GET", "/v1/beds/execd-b/execd"+suffix, "", nil, "")
		if err != nil || other.Status != 404 {
			t.Fatalf("cross-bed leak: %+v %v", other, err)
		}
		for range 2 {
			removed, err := c.request(ctx, "DELETE", base+"/directories?path=/tmp/execd&path=/tmp/missing", headerBed, nil, "")
			if err != nil || removed.Status != 200 {
				t.Fatalf("remove: %+v %v", removed, err)
			}
		}
	}
	missing, err := c.json(ctx, "POST", "/v1/beds/absent/execd/command", "execd-a", map[string]string{"command": "true"}, nil)
	if err != nil || missing.Status != 404 {
		t.Fatalf("missing bed: %+v %v", missing, err)
	}
}

func TestOpenSandboxAdapterLifecycle(t *testing.T) {
	target := startTarget(t, targetOptions{isolation: "dorm"})
	c := target.client
	for _, id := range []string{"adapter-a", "adapter-b"} {
		must2xx(t, "create adapter Bed", c.createBed(t, id))
		c.waitBed(t, id, func(b bedView) bool { return b.Status.Readiness.Ready }, "ready")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for _, base := range []string{"/execd", "/v1/beds/adapter-a/execd"} {
		response, err := c.json(ctx, "POST", base+"/directories", "adapter-a", map[string]any{"/tmp/adapter": map[string]int{"mode": 755}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "mkdir", response)
		must2xx(t, "upload", c.upload(t, "adapter-a", "/tmp/adapter/input", []byte("first\nsecond\n")))
		response, err = c.request(ctx, "GET", base+"/files/download?path=/tmp/adapter/input&offset=2&limit=1", "adapter-a", nil, "")
		if err != nil || response.Status != 200 || string(response.Body) != "second\n" {
			t.Fatalf("line download: %+v %v", response, err)
		}
		var session struct {
			ID string `json:"session_id"`
		}
		response, err = c.json(ctx, "POST", base+"/session", "adapter-a", map[string]string{"cwd": "/tmp/adapter"}, &session)
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "create session", response)
		for _, command := range []string{`export SAVED=value; printf '%s' "$SAVED" > output`, `test "$SAVED" = value && test "$(cat output)" = value`} {
			response, err = c.json(ctx, "POST", base+"/session/"+session.ID+"/run", "adapter-a", map[string]any{"command": command, "timeout": 3000}, nil)
			if err != nil || !strings.Contains(string(response.Body), `"type":"execution_complete"`) {
				t.Fatalf("session state: %+v %v", response, err)
			}
		}
		response, err = c.request(ctx, "POST", "/v1/beds/adapter-b/execd/session/"+session.ID+"/run", "", strings.NewReader(`{"command":"true"}`), "application/json")
		if err != nil || response.Status != 404 {
			t.Fatalf("cross-Bed session: %+v %v", response, err)
		}
		response, err = c.json(ctx, "DELETE", base+"/session/"+session.ID, "adapter-a", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "delete session", response)
		response, err = c.json(ctx, "POST", base+"/command", "adapter-a", map[string]any{"command": "printf detached; sleep 30", "background": true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "background launch", response)
		id := ""
		for _, line := range strings.Split(string(response.Body), "\n") {
			if strings.HasPrefix(line, "data: ") {
				var event struct{ Type, Text string }
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
					t.Fatal(err)
				}
				if event.Type == "init" {
					id = event.Text
				}
			}
		}
		if id == "" {
			t.Fatal("missing execution ID")
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			response, err = c.request(ctx, "GET", base+"/command/"+id+"/logs", "adapter-a", nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if string(response.Body) == "detached" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("live partial log: %+v", response)
			}
			time.Sleep(10 * time.Millisecond)
		}
		response, err = c.json(ctx, "DELETE", base+"/command?id="+id, "adapter-a", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "cancel background", response)
		for {
			var status struct {
				Running  bool `json:"running"`
				ExitCode *int `json:"exit_code"`
			}
			response, err = c.json(ctx, "GET", base+"/command/status/"+id, "adapter-a", nil, &status)
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "status", response)
			if !status.Running && status.ExitCode != nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("background did not stop")
			}
			time.Sleep(10 * time.Millisecond)
		}
		// Cancellation can append a shell diagnostic to stderr. Verify the tail
		// against the final byte stream instead of assuming its length stays 8.
		complete, err := c.request(ctx, "GET", base+"/command/"+id+"/logs", "adapter-a", nil, "")
		if err != nil || complete.Status != 200 || !bytes.HasPrefix(complete.Body, []byte("detached")) {
			t.Fatalf("final log: %+v %v", complete, err)
		}
		cursor := complete.Header.Get("EXECD-COMMANDS-TAIL-CURSOR")
		if cursor != strconv.Itoa(len(complete.Body)) {
			t.Fatalf("final cursor %q disagrees with %d bytes", cursor, len(complete.Body))
		}
		response, err = c.request(ctx, "GET", base+"/command/"+id+"/logs?cursor=8", "adapter-a", nil, "")
		if err != nil || response.Status != 200 || !bytes.Equal(response.Body, complete.Body[8:]) || response.Header.Get("EXECD-COMMANDS-TAIL-CURSOR") != cursor {
			t.Fatalf("tail reconstruction: %+v %v", response, err)
		}
		response, err = c.request(ctx, "GET", base+"/command/"+id+"/logs?cursor="+cursor, "adapter-a", nil, "")
		if err != nil || response.Status != 200 || len(response.Body) != 0 || response.Header.Get("EXECD-COMMANDS-TAIL-CURSOR") != cursor {
			t.Fatalf("tail EOF: %+v %v", response, err)
		}
		response, err = c.json(ctx, "POST", base+"/code/context", "adapter-a", map[string]string{"language": "python"}, nil)
		if err != nil || response.Status != 501 {
			t.Fatalf("unsupported kernel: %+v %v", response, err)
		}
	}
}
