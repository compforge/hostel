//go:build e2e

package e2e_test

import (
	"context"
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

	"github.com/qiankunli/hostel/internal/bed/tool"
)

// This asserts the real kernel contract through public APIs, not a filtered
// process listing: namespace identity, procfs, sessions, Services and bind mounts.
func TestPrivatePIDNamespace(t *testing.T) {
	if os.Getenv("HOSTEL_E2E_REQUIRE_PID_NAMESPACE") != "1" {
		t.Skip("set HOSTEL_E2E_REQUIRE_PID_NAMESPACE=1 on a privileged Linux runner")
	}
	requireTestBinary(t)
	if runtime.GOOS != "linux" {
		t.Fatal("private PID namespace requires Linux")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("Python 3 is required for the HTTP Service fixture")
	}
	options := restrictedOptions()
	options.Bed.Executor.PIDNS = value(tool.Required)
	options.Bed.Filesystem.Bwrap = value(tool.Required)
	c := startTarget(t, targetOptions{isolation: "suite", executor: "supervisor", config: &options}).client
	for _, id := range []string{"process-a", "process-b"} {
		source := t.TempDir()
		if err := os.Chmod(source, 0777); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		response, err := c.json(ctx, "POST", "/v1/beds", "", map[string]any{
			"id":            id,
			"port_mappings": []map[string]any{{"name": "http", "bed_port": 8080, "publish": true}},
			"path_mappings": []map[string]any{{"host_path": source, "bed_path": "/run/application-control"}},
			"services":      []map[string]any{{"name": "worker", "command": []string{"/bin/sh", "-c", `readlink /proc/self/ns/pid > /run/application-control/namespace; readlink /proc/self/ns/mnt > /run/application-control/mount; printf '%s' "$0" > /run/application-control/owner; while :; do sleep 1; done`, id}, "required": true, "restart": "never", "stop_seconds": 1}, {"name": "web", "port_mapping": "http", "command": []string{python, "-u", "-c", portServiceProgram}, "env": map[string]string{"SERVICE_PORT": "${PORT}", "LABEL": id}, "required": true, "http": map[string]string{"ready_path": "/"}}},
		}, nil)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "create process Bed", response)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			response, err := c.json(ctx, "DELETE", "/v1/beds/"+id+"?purge=true", "", nil, nil)
			if err != nil {
				t.Error(err)
				return
			}
			must2xx(t, "purge process Bed", response)
		})
	}
	hostNamespace, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		t.Fatal(err)
	}
	namespaces := map[string]bool{hostNamespace: true}
	for _, id := range []string{"process-a", "process-b"} {
		c.waitBed(t, id, func(b bedView) bool { return b.Status.Readiness.Ready }, "process Service ready")
		// The same control mount and PID domain are visible from every command,
		// session and Service, and procfs exposes only this namespace's processes.
		const script = `set -eu
for i in 1 2 3 4 5; do test -s /run/application-control/namespace && break; sleep 0.1; done
test "$(readlink /proc/self/ns/pid)" = "$(cat /run/application-control/namespace)"
test "$(readlink /proc/self/ns/mnt)" = "$(cat /run/application-control/mount)"
# PID 1 is the trusted root supervisor; unprivileged workloads cannot read
# its namespace symlink. Its proc status must expose namespace-local PID 1.
awk '/^NSpid:/ { found=1; if (NF != 2 || $2 != 1) exit 1 } END { if (!found) exit 1 }' /proc/1/status
awk '$5 == "/run/application-control" { found=1 } END { exit !found }' /proc/self/mountinfo
awk '/^NSpid:/ { if (NF != 2) exit 1 }' /proc/self/status
cat /run/application-control/namespace
cat /run/application-control/owner`
		result, response := c.command(t, id, map[string]any{"command": script, "timeout": 5000})
		must2xx(t, "private procfs command", response)
		assertCommandExit(t, result, 0)
		lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
		if len(lines) != 2 || lines[1] != id || namespaces[lines[0]] {
			t.Fatalf("namespace/control isolation: %q", result.Stdout)
		}
		namespaces[lines[0]] = true
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		var session struct {
			ID string `json:"session_id"`
		}
		response, err := c.json(ctx, "POST", "/session", id, map[string]any{}, &session)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "create private session", response)
		sessionResult, response := c.stream(t, "/session/"+url.PathEscape(session.ID)+"/run", id, map[string]any{"command": script, "timeout": 5000})
		must2xx(t, "private procfs session", response)
		assertCommandExit(t, sessionResult, 0)
		if sessionResult.Stdout != result.Stdout {
			t.Fatalf("session changed PID/control view: %q != %q", sessionResult.Stdout, result.Stdout)
		}
		ctx, cancel = context.WithTimeout(t.Context(), 5*time.Second)
		var detail struct {
			Executor struct {
				Private bool `json:"private_pid_namespace"`
			} `json:"executor"`
		}
		response, err = c.json(ctx, "GET", "/v1/beds/"+id, "", nil, &detail)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "private namespace status", response)
		var ports servicePortsView
		ctx, cancel = context.WithTimeout(t.Context(), 5*time.Second)
		response, err = c.json(ctx, "GET", "/v1/beds/"+id, "", nil, &ports)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "private Service ports", response)
		if len(ports.Status.PortMappings) != 1 || ports.Status.PortMappings[0].State != "listening" {
			t.Fatalf("listener ownership not verified: %+v", ports)
		}
		target, err := url.Parse(c.baseURL)
		if err != nil {
			t.Fatal(err)
		}
		endpoint := "http://" + net.JoinHostPort(target.Hostname(), strconv.Itoa(ports.Status.PortMappings[0].HostPort))
		request, err := http.NewRequestWithContext(t.Context(), "GET", endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		reply, err := c.http.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reply.Body)
		reply.Body.Close()
		if err != nil || reply.StatusCode != 200 || string(body) != id {
			t.Fatalf("Service endpoint: status=%d body=%q err=%v", reply.StatusCode, body, err)
		}
		t.Logf("Bed %s: namespace=%s Service port=%d", id, lines[0], ports.Status.PortMappings[0].HostPort)
		if !detail.Executor.Private {
			t.Fatal("realized private PID namespace not reported")
		}
	}
}
