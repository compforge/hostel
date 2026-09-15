//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/filesystem"
	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/bed/resource"
	"github.com/qiankunli/hostel/internal/bed/tool"
	"github.com/qiankunli/hostel/internal/config"
)

func value[T any](v T) *T { return &v }
func restrictedOptions() config.Options {
	return config.Options{Bed: config.BedOptions{
		Filesystem: filesystem.Options{Bwrap: value(tool.Off), Landlock: value(tool.Off), UID: value(tool.Off), PRoot: value(tool.Off), Pathshim: value(tool.Off)},
		Network:    network.Options{NetNS: value(tool.Off)}, Resource: resource.Options{Cgroup: value(tool.Off)},
	}}
}
func requireTestBinary(t *testing.T) string {
	t.Helper()
	binary := os.Getenv(binaryEnv)
	if binary == "" {
		t.Skip("internal tool configuration runs through make e2e, not production images")
	}
	return binary
}
func TestFeaturePoliciesOff(t *testing.T) {
	requireTestBinary(t)
	options := restrictedOptions()
	c := startTarget(t, targetOptions{isolation: "suite", config: &options}).client
	var status struct {
		Components struct {
			Filesystem struct {
				Effective string
				Tools     map[string]tool.Status
			}
			Network struct {
				Enabled bool
				Tools   map[string]tool.Status
			}
			Resource struct {
				Accounting struct {
					Available bool
					Tools     map[string]tool.Status
				}
			}
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	response, err := c.json(ctx, "GET", "/v1/status", "", nil, &status)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "tool status", response)
	if status.Components.Filesystem.Effective != "shared" || status.Components.Network.Enabled || status.Components.Resource.Accounting.Available {
		t.Fatalf("disabled tools active: %+v", status)
	}
	for _, tools := range []map[string]tool.Status{status.Components.Filesystem.Tools, status.Components.Network.Tools, status.Components.Resource.Accounting.Tools} {
		if len(tools) == 0 {
			t.Fatal("missing tool diagnostics")
		}
		for name, f := range tools {
			if f.Policy != tool.Off || f.Probe != "not_probed" || f.Selected || f.Reason != "disabled_by_config" {
				t.Fatalf("%s: %+v", name, f)
			}
		}
	}
	must2xx(t, "create unisolated Bed", c.createBed(t, "tools-off"))
	result, response := c.command(t, "tools-off", map[string]any{"command": "printf tool-policy-ok", "timeout": 5000})
	must2xx(t, "command with optional tools disabled", response)
	assertCommandExit(t, result, 0)
	if result.Stdout != "tool-policy-ok" {
		t.Fatal(result.Stdout)
	}
	response, err = c.json(ctx, "GET", "/v1/beds/tools-off/network/policy", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != http.StatusServiceUnavailable {
		t.Fatalf("disabled network status=%d body=%s; want 503", response.Status, response.Body)
	}
}
func TestFeatureRequiredStartupFailure(t *testing.T) {
	binary := requireTestBinary(t)
	for _, scenario := range []string{"missing-pathshim", "conflicting-boundaries"} {
		t.Run(scenario, func(t *testing.T) {
			options := restrictedOptions()
			expected := "filesystem.pathshim required"
			if scenario == "missing-pathshim" {
				options.Bed.Filesystem.Pathshim = value(tool.Required)
			} else {
				options.Bed.RoomType = value("room")
				options.Bed.Filesystem.Bwrap = value(tool.Required)
				expected = "bwrap requires private files"
			}
			raw, err := json.Marshal(options)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), "startup.json")
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "--beds-root", filepath.Join(t.TempDir(), "beds"))
			// No helper can be discovered. All other optional probes are off; the
			// expected failure occurs before BedUser or executor preparation.
			cmd.Env = append(os.Environ(), "PATH="+t.TempDir(), "HOSTEL_E2E_CONFIG="+file)
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("daemon did not reject startup promptly: %s", output)
			}
			if err == nil || !strings.Contains(string(output), expected) {
				t.Fatalf("startup error=%v output=%s", err, output)
			}
		})
	}
}
