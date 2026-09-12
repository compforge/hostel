//go:build e2e

package e2e_test

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNetworkNamespaces(t *testing.T) {
	if os.Getenv("HOSTEL_E2E_REQUIRE_NETWORK") != "1" {
		t.Skip("set HOSTEL_E2E_REQUIRE_NETWORK=1 in a disposable capable Linux container")
	}
	for _, backend := range []string{"local", "supervisor"} {
		for _, level := range []string{"dorm", "room", "suite"} {
			t.Run(backend+"/"+level, func(t *testing.T) { testNetworkEnvironment(t, backend, level) })
		}
	}
}

func testNetworkEnvironment(t *testing.T, backend, level string) {
	target := startTarget(t, targetOptions{isolation: level, executor: backend})
	c := target.client
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var diagnostics struct {
		Network struct {
			Enabled bool   `json:"enabled"`
			Scope   string `json:"scope"`
			Reason  string `json:"reason"`
		} `json:"network"`
	}
	response, err := c.json(ctx, "GET", "/v1/status", "", nil, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "diagnostics", response)
	if !diagnostics.Network.Enabled || diagnostics.Network.Scope != "bed_processes" {
		t.Fatalf("network: %+v", diagnostics.Network)
	}
	var health healthView
	response, err = c.json(ctx, "GET", "/healthz", "", nil, &health)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "health", response)
	if requiredIsolationLevels()[level] && health.Isolation.Effective != level {
		t.Fatalf("required %s degraded: %+v", level, health.Isolation)
	}
	t.Logf("executor=%s file=%s mechanism=%s", backend, health.Isolation.Effective, health.Isolation.Mechanism)
	check, response := c.command(t, "network-a", map[string]any{"command": "grep -E '^(CapEff|CapPrm|CapInh|CapAmb|CapBnd|NoNewPrivs):' /proc/self/status", "timeout": 30000})
	must2xx(t, "privilege probe", response)
	assertCommandExit(t, check, 0)
	lines := strings.Split(strings.TrimSpace(check.Stdout), "\n")
	if len(lines) != 6 {
		t.Fatalf("incomplete privilege status: %q", check.Stdout)
	}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("bad privilege status: %q", line)
		}
		expected := "0000000000000000"
		if fields[0] == "NoNewPrivs:" {
			expected = "1"
		}
		if fields[1] != expected {
			t.Fatalf("privilege leaked: %s", line)
		}
	}
	ids := make(map[string]string)
	for _, bed := range []string{"network-a", "network-b"} {
		result, response := c.command(t, bed, map[string]any{"command": "readlink /proc/self/ns/net", "timeout": 30000})
		must2xx(t, "network command", response)
		assertCommandExit(t, result, 0)
		ids[bed] = strings.TrimSpace(result.Stdout)
	}
	if ids["network-a"] == ids["network-b"] {
		t.Fatal("commands share netns")
	}
	var session struct {
		ID string `json:"session_id"`
	}
	response, err = c.json(ctx, "POST", "/session", "network-a", map[string]string{}, &session)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "create session", response)
	run, response := c.stream(t, "/session/"+url.PathEscape(session.ID)+"/run", "network-a", map[string]any{"command": "readlink /proc/self/ns/net", "timeout": 30000})
	must2xx(t, "session netns", response)
	assertCommandExit(t, run, 0)
	if strings.TrimSpace(run.Stdout) != ids["network-a"] {
		t.Fatalf("shell and command disagree: %q != %q", run.Stdout, ids["network-a"])
	}
	response, err = c.json(ctx, "DELETE", "/v1/beds/network-a?purge=true", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "purge network", response)
	run, response = c.command(t, "network-a", map[string]any{"command": "echo recreated", "timeout": 30000})
	must2xx(t, "recreate network", response)
	assertCommandExit(t, run, 0)
}
