//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/go-stdx/shellx"
	"github.com/qiankunli/hostel/internal/bed/tool"
	"github.com/qiankunli/hostel/internal/config"
)

type pillowPortsView struct {
	Status struct {
		Subsystems map[string]json.RawMessage `json:"subsystems"`
		Pillows    []struct {
			Name        string   `json:"name"`
			Ready       bool     `json:"ready"`
			ExecutionID string   `json:"execution_id"`
			Ports       []string `json:"ports"`
		} `json:"pillows"`
		PortMappings []struct {
			Name        string `json:"name"`
			Network     string `json:"network"`
			State       string `json:"state"`
			BedPort     int    `json:"bed_port"`
			HostPort    int    `json:"host_port"`
			ExecutionID string `json:"execution_id"`
			Reason      string `json:"reason"`
		} `json:"port_mappings"`
	} `json:"status"`
}

// This fixture is an ordinary HTTP program launched through the Pillow API.
// The same endpoints are checked from both a Bed command and an external client.
const portPillowProgram = `import http.server, os, sys
class Handler(http.server.BaseHTTPRequestHandler):
 def do_GET(self):
  self.send_response(200)
  self.end_headers()
  self.wfile.write(os.environ["LABEL"].encode())
 def log_message(self, *args): pass
http.server.HTTPServer(("0.0.0.0", int(sys.argv[1])), Handler).serve_forever()
`

func TestBedPortMappings(t *testing.T) {
	if os.Getenv(binaryEnv) == "" {
		t.Skip("requires the binary profile with Python 3")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 fixture unavailable")
	}
	for _, mode := range []string{"shared", "private"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "private" && os.Getenv("HOSTEL_E2E_REQUIRE_NETWORK") != "1" {
				t.Skip("requires disposable Linux network profile")
			}
			for _, executor := range []string{"local", "supervisor"} {
				t.Run(executor, func(t *testing.T) {
					if executor == "supervisor" && runtime.GOOS != "linux" {
						t.Skip("supervisor requires Linux")
					}
					testBedPortMappings(t, mode, executor, python)
				})
			}
		})
	}
}
func testBedPortMappings(t *testing.T, mode, executor, python string) {
	options := config.Options{}
	if mode == "shared" {
		options.Bed.Network.NetNS = value(tool.Off)
	} else {
		options.Bed.Network.NetNS = value(tool.Required)
	}
	c := startTarget(t, targetOptions{executor: executor, config: &options}).client
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	request := func(method, path string, body, out any) httpResult {
		t.Helper()
		response, err := c.json(ctx, method, path, "", body, out)
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, path, response)
		return response
	}
	read := func(name string) pillowPortsView {
		t.Helper()
		var d pillowPortsView
		request("GET", "/v1/beds/"+name, nil, &d)
		return d
	}
	check := func(name string) pillowPortsView {
		t.Helper()
		d := read(name)
		if d.Status.Subsystems["pillows"] != nil || d.Status.Subsystems["network"] == nil || len(d.Status.Pillows) != 1 || len(d.Status.PortMappings) != 1 {
			t.Fatalf("status layout: %+v", d)
		}
		s, p := d.Status.Pillows[0], d.Status.PortMappings[0]
		if !s.Ready || p.State != "listening" || p.Network != mode || p.ExecutionID != s.ExecutionID || p.Name != "http" {
			t.Fatalf("runtime identity: %+v", d)
		}
		if mode == "shared" && (p.BedPort != p.HostPort || p.BedPort == 8080) {
			t.Fatalf("shared mapping: %+v", p)
		}
		if mode == "private" && p.BedPort != 8080 {
			t.Fatalf("private preferred port: %+v", p)
		}
		var access struct {
			Access struct {
				Ports []struct {
					Name        string `json:"name"`
					ExecutionID string `json:"execution_id"`
					BedPort     int    `json:"bed_port"`
					HostPort    int    `json:"host_port"`
				} `json:"ports"`
			} `json:"access"`
			Hold struct {
				ID string `json:"id"`
			} `json:"hold"`
		}
		request("POST", "/v1/beds/"+name+"/pillows/web/access", map[string]int{"hold_seconds": 30}, &access)
		if access.Access.Ports[0].HostPort != p.HostPort || access.Access.Ports[0].BedPort != p.BedPort || access.Access.Ports[0].Name != s.Ports[0] || access.Access.Ports[0].ExecutionID != s.ExecutionID || access.Hold.ID == "" {
			t.Fatalf("access disagrees with status: %+v", access)
		}
		target, err := url.Parse(c.baseURL)
		if err != nil {
			t.Fatal(err)
		}
		hostURL := "http://" + net.JoinHostPort(target.Hostname(), strconv.Itoa(access.Access.Ports[0].HostPort))
		req, err := http.NewRequestWithContext(ctx, "GET", hostURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := c.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || string(body) != name {
			t.Fatalf("external pillow: status=%d body=%s err=%v", response.StatusCode, body, err)
		}
		script := fmt.Sprintf("import urllib.request; print(urllib.request.urlopen(%q, timeout=3).read().decode())", "http://"+net.JoinHostPort("127.0.0.1", strconv.Itoa(access.Access.Ports[0].BedPort)))
		result, responseCommand := c.command(t, name, map[string]any{"command": shellx.Quote(python) + " -c " + shellx.Quote(script), "timeout": 5000})
		must2xx(t, "internal pillow", responseCommand)
		assertCommandExit(t, result, 0)
		if strings.TrimSpace(result.Stdout) != name {
			t.Fatalf("internal pillow: %+v", result)
		}
		request("DELETE", "/v1/beds/"+name+"/pillow-holds/"+access.Hold.ID, nil, nil)
		return d
	}
	portSpec := map[string]any{"name": "http", "protocol": "TCP", "publish": true, "dynamic": true}
	if mode == "private" {
		delete(portSpec, "dynamic")
		portSpec["containerPort"] = 8080
	}
	for _, name := range []string{"ports-a", "ports-b"} {
		request("POST", "/v1/beds", map[string]any{"id": name,

			"pillows": []any{map[string]any{"name": "web", "ports": []any{portSpec}, "command": []string{python, "-u", "-c", portPillowProgram, "${PORT}"}, "env": map[string]string{"LABEL": name}, "required": true, "readinessProbe": map[string]any{"httpGet": map[string]any{"path": "/", "port": "http"}, "periodSeconds": 1}}},
		}, nil)
		c.waitBed(t, name, func(b bedView) bool { return b.Status.Readiness.Ready }, "pillow ready")
	}
	a, b := check("ports-a"), check("ports-b")
	if a.Status.PortMappings[0].HostPort == b.Status.PortMappings[0].HostPort {
		t.Fatal("Beds share host port")
	}
	request("POST", "/v1/beds/ports-a/pillows/web/process/restart", nil, nil)
	deadline := time.Now().Add(15 * time.Second)
	for {
		current := read("ports-a")
		if len(current.Status.Pillows) == 1 && current.Status.Pillows[0].Ready && current.Status.Pillows[0].ExecutionID != a.Status.Pillows[0].ExecutionID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restart timeout: %+v", current)
		}
		time.Sleep(50 * time.Millisecond)
	}
	check("ports-a")
	check("ports-b")
	for _, name := range []string{"ports-a", "ports-b"} {
		request("DELETE", "/v1/beds/"+name, nil, nil)
	}
	var status struct {
		Host struct {
			Status struct {
				Ports []struct {
					Owner string `json:"owner"`
				} `json:"ports"`
			} `json:"status"`
		} `json:"host"`
	}
	request("GET", "/v1/status", nil, &status)
	for _, p := range status.Host.Status.Ports {
		if strings.Contains(p.Owner, "/port/") {
			t.Fatalf("eviction leaked pillow port: %+v", p)
		}
	}
}
