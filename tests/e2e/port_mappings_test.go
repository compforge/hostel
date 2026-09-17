//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/go-stdx/shellx"
)

type servicePortsView struct {
	Status struct {
		Components map[string]json.RawMessage `json:"components"`
		Services   []struct {
			Name         string `json:"name"`
			Ready        bool   `json:"ready"`
			ExecutionID  string `json:"execution_id"`
			HostEndpoint string `json:"host_endpoint"`
			BedEndpoint  string `json:"bed_endpoint"`
		} `json:"services"`
		PortMappings []struct {
			Name        string `json:"name"`
			Network     string `json:"network"`
			State       string `json:"state"`
			BedPort     int    `json:"bed_port"`
			HostPort    int    `json:"host_port"`
			BedAddress  string `json:"bed_address"`
			HostAddress string `json:"host_address"`
			ExecutionID string `json:"execution_id"`
			Reason      string `json:"reason"`
		} `json:"port_mappings"`
	} `json:"status"`
}

// This fixture is an ordinary HTTP program launched through the Service API.
// The same endpoints are checked from both a Bed command and an external client.
const portServiceProgram = `import http.server, os
class Handler(http.server.BaseHTTPRequestHandler):
 def do_GET(self):
  self.send_response(200)
  self.end_headers()
  self.wfile.write(os.environ["LABEL"].encode())
 def log_message(self, *args): pass
http.server.HTTPServer(("0.0.0.0", int(os.environ["SERVICE_PORT"])), Handler).serve_forever()
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
	isolation := "dorm"
	if mode == "private" {
		isolation = "suite"
	}
	c := startTarget(t, targetOptions{isolation: isolation, executor: executor}).client
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
	read := func(name string) servicePortsView {
		t.Helper()
		var d servicePortsView
		request("GET", "/v1/beds/"+name, nil, &d)
		return d
	}
	check := func(name string) servicePortsView {
		t.Helper()
		d := read(name)
		if d.Status.Components["services"] != nil || d.Status.Components["network"] == nil || len(d.Status.Services) != 1 || len(d.Status.PortMappings) != 1 {
			t.Fatalf("status layout: %+v", d)
		}
		s, p := d.Status.Services[0], d.Status.PortMappings[0]
		if !s.Ready || p.State != "listening" || p.Network != mode || p.ExecutionID != s.ExecutionID || p.Name != "http" {
			t.Fatalf("runtime identity: %+v", d)
		}
		if p.BedAddress != fmt.Sprintf("127.0.0.1:%d", p.BedPort) || p.HostAddress == "" || s.BedEndpoint != "http://"+p.BedAddress || s.HostEndpoint != "http://"+p.HostAddress {
			t.Fatalf("service and mapping addresses disagree: %+v", d)
		}
		if mode == "shared" && (p.BedPort != p.HostPort || p.BedPort == 8080 || p.Reason != "SharedNetworkUsesHostPort") {
			t.Fatalf("shared mapping: %+v", p)
		}
		if mode == "private" && p.BedPort != 8080 {
			t.Fatalf("private preferred port: %+v", p)
		}
		var access struct {
			Access struct {
				HostEndpoint string `json:"host_endpoint"`
				BedEndpoint  string `json:"bed_endpoint"`
				ExecutionID  string `json:"execution_id"`
			} `json:"access"`
			Hold struct {
				ID string `json:"id"`
			} `json:"hold"`
		}
		request("POST", "/v1/beds/"+name+"/services/web/access", map[string]int{"hold_seconds": 30}, &access)
		if access.Access.HostEndpoint != s.HostEndpoint || access.Access.BedEndpoint != s.BedEndpoint || access.Access.ExecutionID != s.ExecutionID || access.Hold.ID == "" {
			t.Fatalf("access disagrees with status: %+v", access)
		}
		req, err := http.NewRequestWithContext(ctx, "GET", s.HostEndpoint, nil)
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
			t.Fatalf("external service: status=%d body=%s err=%v", response.StatusCode, body, err)
		}
		script := fmt.Sprintf("import urllib.request; print(urllib.request.urlopen(%q, timeout=3).read().decode())", s.BedEndpoint)
		result, responseCommand := c.command(t, name, map[string]any{"command": shellx.Quote(python) + " -c " + shellx.Quote(script), "timeout": 5000})
		must2xx(t, "internal service", responseCommand)
		assertCommandExit(t, result, 0)
		if strings.TrimSpace(result.Stdout) != name {
			t.Fatalf("internal service: %+v", result)
		}
		request("DELETE", "/v1/beds/"+name+"/service-holds/"+access.Hold.ID, nil, nil)
		return d
	}
	for _, name := range []string{"ports-a", "ports-b"} {
		request("POST", "/v1/beds", map[string]any{"id": name,
			"port_mappings": []any{map[string]any{"name": "http", "bed_port": 8080, "publish": true}},
			"services":      []any{map[string]any{"name": "web", "port_mapping": "http", "command": []string{python, "-u", "-c", portServiceProgram}, "env": map[string]string{"SERVICE_PORT": "${PORT}", "LABEL": name}, "required": true, "http": map[string]string{"ready_path": "/"}}},
		}, nil)
		c.waitBed(t, name, func(b bedView) bool { return b.Status.Readiness.Ready }, "service ready")
	}
	a, b := check("ports-a"), check("ports-b")
	if a.Status.PortMappings[0].HostPort == b.Status.PortMappings[0].HostPort {
		t.Fatal("Beds share host port")
	}
	request("POST", "/v1/beds/ports-a/services/web/restart", nil, nil)
	deadline := time.Now().Add(15 * time.Second)
	for {
		current := read("ports-a")
		if len(current.Status.Services) == 1 && current.Status.Services[0].Ready && current.Status.Services[0].ExecutionID != a.Status.Services[0].ExecutionID {
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
			t.Fatalf("eviction leaked service port: %+v", p)
		}
	}
}
