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
	target := startTarget(t, targetOptions{})
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
	response, err := c.json(ctx, "GET", "/v1/diagnostics", "", nil, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "diagnostics", response)
	if !diagnostics.Network.Enabled || diagnostics.Network.Scope != "bed_processes" {
		t.Fatalf("network: %+v", diagnostics.Network)
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
