//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/feature"
)

// Opt-in because this asserts host-privileged preparation, not a rootless
// substitute. Existing Bed-root cases exercise the same workloads in both modes.
func TestRootfulPreparedMount(t *testing.T) {
	if os.Getenv("HOSTEL_E2E_REQUIRE_ROOTFUL") != "1" {
		t.Skip("set HOSTEL_E2E_REQUIRE_ROOTFUL=1 on a rootful Linux runner")
	}
	requireTestBinary(t)
	if runtime.GOOS != "linux" {
		t.Fatal("rootful preparation requires Linux")
	}
	for _, backend := range []string{"local", "supervisor"} {
		t.Run(backend, func(t *testing.T) {
			options := restrictedOptions()
			options.Bed.Filesystem.Bwrap = value(feature.Required)
			c := startTarget(t, targetOptions{isolation: "suite", executor: backend, config: &options}).client
			const bed = "rootful"
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			response, err := c.json(ctx, "POST", "/v1/beds", "", map[string]any{
				"id": bed, "env": map[string]string{"MARKER": "workload-env", "GODEBUG": "inittrace=0"},
				"services": []map[string]any{{"name": "observer", "command": []string{"/bin/sh", "-c", `readlink /proc/self/ns/mnt > /workspace/service-mount; id -u > /workspace/service-uid; grep -E '^(CapInh|CapPrm|CapEff|CapBnd|CapAmb|NoNewPrivs):' /proc/self/status > /workspace/service-caps; exec sleep 300`}, "required": true, "restart": "never", "stop_seconds": 1}},
			}, nil)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "create rootful Bed", response)
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				response, err := c.json(ctx, "DELETE", "/v1/beds/"+bed+"?purge=true", "", nil, nil)
				if err != nil {
					t.Error(err)
					return
				}
				must2xx(t, "purge prepared Bed", response)
			})
			c.waitBed(t, bed, func(b bedView) bool { return b.Status.Readiness.Ready }, "rootful Service ready")
			// Different executions must enter the same prepared mount namespace,
			// including the resident Service and session shell.
			const script = `set -eu
test "$MARKER" = workload-env
for i in 1 2 3 4 5; do test -s /workspace/service-caps && break; sleep 0.1; done
test "$(id -u)" != 0
test "$(id -u)" = "$(cat /workspace/service-uid)"
test "$(readlink /proc/self/ns/mnt)" = "$(cat /workspace/service-mount)"
readlink /proc/self/ns/mnt
grep -E '^(CapInh|CapPrm|CapEff|CapBnd|CapAmb|NoNewPrivs):' /proc/self/status
cat /workspace/service-caps`
			var namespace string
			check := func(result commandResult) {
				assertCommandExit(t, result, 0)
				lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
				if len(lines) != 13 {
					t.Fatalf("missing credential evidence: %q", result.Stdout)
				}
				if namespace == "" {
					namespace = lines[0]
				} else if lines[0] != namespace {
					t.Fatalf("mount namespace changed: %q != %q", lines[0], namespace)
				}
				for _, line := range lines[1:] {
					fields := strings.Fields(line)
					want := "0000000000000000"
					if fields[0] == "NoNewPrivs:" {
						want = "1"
					}
					if len(fields) != 2 || fields[1] != want {
						t.Fatalf("unsafe workload credentials: %s", line)
					}
				}
			}
			for i := 0; i < 2; i++ {
				result, response := c.command(t, bed, map[string]any{"command": script, "timeout": 5000})
				must2xx(t, "prepared command", response)
				check(result)
			}
			ctx, cancel = context.WithTimeout(t.Context(), 5*time.Second)
			var session struct {
				ID string `json:"session_id"`
			}
			response, err = c.json(ctx, "POST", "/session", bed, map[string]any{}, &session)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "create session", response)
			if session.ID == "" {
				t.Fatal("missing session id")
			}
			result, response := c.stream(t, "/session/"+url.PathEscape(session.ID)+"/run", bed, map[string]any{"command": script, "timeout": 5000})
			must2xx(t, "prepared session", response)
			check(result)
			t.Log(fmt.Sprintf("shared prepared mount=%s; command/session/Service credentials verified", namespace))
		})
	}
}
