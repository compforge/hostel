//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/tool"
)

func TestSuiteWithSharedCapabilities(t *testing.T) {
	if os.Getenv("HOSTEL_E2E_REQUIRE_ROOTFUL") != "1" {
		t.Skip("requires rootful Linux file isolation")
	}
	requireTestBinary(t)
	for _, scenario := range []string{"network-off", "network-probe-failure"} {
		t.Run(scenario, func(t *testing.T) {
			options := restrictedOptions()
			options.Bed.Filesystem.Bwrap = value(tool.Required)
			options.Bed.Privilege.UID, options.Bed.Privilege.GID = value(1000), value(1000)
			options.Bed.Executor.PIDNS = value(tool.Off)
			targetConfig := targetOptions{isolation: "suite", executor: "supervisor", config: &options}
			if scenario == "network-probe-failure" {
				options.Bed.Network.NetNS = value(tool.Auto)
				helpers := t.TempDir()
				// A run-owned failing tool exercises the real network probe and fallback;
				// no host firewall state or installed tool is modified.
				if err := os.WriteFile(filepath.Join(helpers, "nft"), []byte("#!/bin/sh\necho 'fixture: nft unavailable' >&2\nexit 1\n"), 0755); err != nil {
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
				"id":       bed,
				"services": []map[string]any{{"name": "writer", "command": []string{"/bin/sh", "-c", "printf service > /workspace/service-marker; exec /bin/sleep 300"}, "required": true, "restart": "never", "stop_seconds": 1}},
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "create suite", response)
			c.waitBed(t, bed, func(b bedView) bool { return b.Status.Readiness.Ready }, "suite Service ready")
			waitRootFile(t, c, bed, "/workspace/service-marker", "service")
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
			result, response = c.stream(t, "/session/"+url.PathEscape(session.ID)+"/run", bed, map[string]any{"command": "printf session > /workspace/session-marker; test \"$(/bin/cat /workspace/service-marker)\" = service", "timeout": 5000})
			must2xx(t, "suite session", response)
			assertCommandExit(t, result, 0)
			waitRootFile(t, c, bed, "/workspace/session-marker", "session")
			result, response = c.command(t, "suite-neighbor", map[string]any{"command": "/bin/cat \"$TARGET\"", "envs": map[string]string{"TARGET": filepath.Join(target.bedsRoot, bed, "data/workspace/command-marker")}, "timeout": 5000})
			must2xx(t, "sibling file access", response)
			if result.Result == nil || result.Result.Process.ExitCode == nil || *result.Result.Process.ExitCode == 0 {
				t.Fatalf("suite exposed sibling file: %+v", result)
			}
			assertSuiteStatus(t, c, bed)
			var status struct {
				Components struct {
					Filesystem struct{ Effective string }
					Privilege  struct{ Selection struct{ Effective string } }
					Network    struct {
						Effective, Reason string
						Tools             map[string]tool.Status
					}
					Executor struct{ Tools map[string]tool.Status }
				}
			}
			response, err = c.json(ctx, "GET", "/v1/status", "", nil, &status)
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "suite component facts", response)
			if status.Components.Filesystem.Effective != "private" || status.Components.Privilege.Selection.Effective != "shared" || status.Components.Network.Effective != "shared" || status.Components.Executor.Tools["pidns"].Selected {
				t.Fatalf("unexpected components: %+v", status)
			}
			if scenario == "network-probe-failure" && (status.Components.Network.Tools["netns"].Probe != "unavailable" || status.Components.Network.Reason == "") {
				t.Fatalf("missing failed probe evidence: %+v", status)
			}
			// File-suite does not satisfy an explicit network-policy requirement.
			response, err = c.json(ctx, "POST", "/v1/beds", "", map[string]any{"id": "needs-policy", "networkPolicy": map[string]any{"defaultAction": "deny"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if response.Status == http.StatusAccepted {
				c.waitBed(t, "needs-policy", func(b bedView) bool { return b.Status.Phase == "failed" && !b.Status.Readiness.Ready }, "unavailable required network policy fails")
			} else if response.Status < 400 {
				t.Fatalf("network policy silently accepted: %+v", response)
			}
		})
	}
}

func assertSuiteStatus(t *testing.T, c *apiClient, bed string) {
	t.Helper()
	type room struct {
		Requested, Effective string
		Reasons              []string
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
			if body.Instance.Isolation != "suite" {
				t.Fatalf("inventory: %+v", body)
			}
			continue
		}
		got := body.Isolation
		if strings.HasPrefix(endpoint, "/v1/beds/") {
			got = body.Status.Isolation
		}
		if got.Requested != "suite" || got.Effective != "suite" || len(got.Reasons) != 0 {
			t.Fatalf("%s: %+v", endpoint, got)
		}
	}
}
