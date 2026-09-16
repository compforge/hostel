//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"net/url"
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
