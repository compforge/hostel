//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/go-stdx/shellx"
	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/bed/privilege"
	"github.com/qiankunli/hostel/internal/bed/tool"
	"github.com/qiankunli/hostel/protocol"
)

func TestPrivateFilesWithSharedNetwork(t *testing.T) {
	if os.Getenv("HOSTEL_E2E_REQUIRE_ROOTFUL") != "1" {
		t.Skip("requires rootful Linux file isolation")
	}
	requireTestBinary(t)
	for _, scenario := range []string{"network-off", "network-probe-failure"} {
		t.Run(scenario, func(t *testing.T) {
			options := restrictedOptions()
			options.Bed.Filesystem.Bwrap = value(tool.Required)
			options.Bed.Network.UIDNFT = value(tool.Off)
			options.Bed.Executor.PIDNS = value(tool.Off)
			targetConfig := targetOptions{executor: "supervisor", config: &options}
			if scenario == "network-probe-failure" {
				options.Bed.Network.NetNS = value(tool.Auto)
				helpers := t.TempDir()
				// Fail rule installation but preserve real cleanup. Failing every nft
				// call exercises retained cleanup ownership, not best-effort fallback.
				nft, err := exec.LookPath("nft")
				if err != nil {
					t.Fatal(err)
				}
				script := "#!/bin/sh\nset -eu\nif [ \"${1:-}\" = -f ] && [ \"${2:-}\" = - ]; then\n" +
					"rules=$(cat)\ncase \"$rules\" in *'{'*) echo 'fixture: nft rule installation unavailable' >&2; exit 1;; esac\n" +
					"printf '%s\\n' \"$rules\" | " + shellx.Quote(nft) + " \"$@\"\nelse\nexec " + shellx.Quote(nft) + " \"$@\"\nfi\n"
				if err := os.WriteFile(filepath.Join(helpers, "nft"), []byte(script), 0755); err != nil {
					t.Fatal(err)
				}
				targetConfig.helperPath = helpers + ":" + os.Getenv("PATH")
			}
			target := startTarget(t, targetConfig)
			c := target.client
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			const bed = "suite-shared"
			response, err := c.json(ctx, "POST", "/v1/beds", "", map[string]any{
				"id":      bed,
				"pillows": []map[string]any{{"name": "writer", "command": []string{"/bin/sh", "-c", fmt.Sprintf("awk '/^Groups:/ { if (NF != 2 || $2 != %d) exit 1 }' /proc/self/status && printf pillow > /workspace/pillow-marker && exec /bin/sleep 300", privilege.SoftwareGID)}, "required": true, "restart_policy": "never", "stop_seconds": 1}},
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "create suite", response)
			c.waitBed(t, bed, func(b bedView) bool { return b.Status.Readiness.Ready }, "suite Pillow ready")
			waitRootFile(t, c, bed, "/workspace/pillow-marker", "pillow")
			result, response := c.command(t, bed, map[string]any{"command": "printf command > /workspace/command-marker; readlink /proc/self/ns/net; readlink /proc/self/ns/pid", "timeout": 5000})
			must2xx(t, "suite command", response)
			assertCommandExit(t, result, 0)
			netNS, err := os.Readlink("/proc/self/ns/net")
			if err != nil {
				t.Fatal(err)
			}
			pidNS, err := os.Readlink("/proc/self/ns/pid")
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(result.Stdout) != netNS+"\n"+pidNS {
				t.Fatalf("expected shared namespaces: %q", result.Stdout)
			}
			waitRootFile(t, c, bed, "/workspace/command-marker", "command")
			var session struct {
				ID string `json:"session_id"`
			}
			response, err = c.json(ctx, "POST", "/session", bed, map[string]any{}, &session)
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "open suite session", response)
			result, response = c.stream(t, "/session/"+url.PathEscape(session.ID)+"/run", bed, map[string]any{"command": "printf session > /workspace/session-marker; test \"$(/bin/cat /workspace/pillow-marker)\" = pillow", "timeout": 5000})
			must2xx(t, "suite session", response)
			assertCommandExit(t, result, 0)
			waitRootFile(t, c, bed, "/workspace/session-marker", "session")
			must2xx(t, "declare Bed", c.createBed(t, "suite-neighbor"))
			result, response = c.command(t, "suite-neighbor", map[string]any{"command": "/bin/cat \"$TARGET\"", "envs": map[string]string{"TARGET": filepath.Join(target.bedsRoot, bed, "volumes/workspace/command-marker")}, "timeout": 5000})
			must2xx(t, "sibling file access", response)
			if result.Result == nil || result.Result.Process.ExitCode == nil || *result.Result.Process.ExitCode == 0 {
				t.Fatalf("suite exposed sibling file: %+v", result)
			}
			assertMixedIsolationStatus(t, c, bed)
			var status struct {
				Subsystems struct {
					Filesystem struct{ Effective string }
					Privilege  struct{ Selection struct{ Effective string } }
					Network    network.Status
					Executor   struct{ Tools map[string]tool.Status }
				}
			}
			response, err = c.json(ctx, "GET", "/v1/status", "", nil, &status)
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "suite subsystem facts", response)
			if status.Subsystems.Filesystem.Effective != "private" || status.Subsystems.Privilege.Selection.Effective != "dedicated" || status.Subsystems.Network.Isolation.Level != "shared" || status.Subsystems.Executor.Tools["pidns"].Selected {
				t.Fatalf("unexpected subsystems: %+v", status)
			}
			if scenario == "network-probe-failure" && (status.Subsystems.Network.Tools["netns"].Probe != "unavailable" || status.Subsystems.Network.Isolation.Diagnostic == nil) {
				t.Fatalf("missing failed probe evidence: %+v", status)
			}
			// Dorm preserves a desired policy without pretending the shared network enforces it.
			response, err = c.json(ctx, "POST", "/v1/beds", "", map[string]any{"id": "needs-policy", "networkPolicy": map[string]any{"defaultAction": "deny"}, "pillows": workspacePillows()}, nil)
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "best effort policy", response)
			c.waitBed(t, "needs-policy", func(b bedView) bool { return b.Status.Readiness.Ready }, "dorm policy Bed ready")
			var policy protocol.EgressStatus
			response, err = c.json(ctx, "GET", "/v1/beds/needs-policy/network/policy", "", nil, &policy)
			if err != nil || response.Status != http.StatusOK || policy.State != protocol.EgressUnenforced || policy.Backend != "none" || policy.Effective != nil {
				t.Fatalf("missing policy gap: %v %+v", err, response)
			}

		})
	}
}

func assertMixedIsolationStatus(t *testing.T, c *apiClient, bed string) {
	t.Helper()
	type room struct {
		Effective string
	}
	for _, endpoint := range []string{"/healthz", "/v1/status", "/v1/beds", "/v1/beds/" + url.PathEscape(bed)} {
		var body struct {
			Isolation room
			Instance  struct{ Isolation string }
			Status    struct{ Isolation room }
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		response, err := c.json(ctx, "GET", endpoint, "", nil, &body)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "suite status", response)
		if endpoint == "/v1/beds" {
			if body.Instance.Isolation != "dorm" {
				t.Fatalf("inventory: %+v", body)
			}
			continue
		}
		got := body.Isolation
		if strings.HasPrefix(endpoint, "/v1/beds/") {
			got = body.Status.Isolation
		}
		if got.Effective != "dorm" {
			t.Fatalf("%s: %+v", endpoint, got)
		}
	}
}
