//go:build e2e && linux

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/bed/tool"
	"github.com/qiankunli/hostel/internal/config"
	"github.com/qiankunli/hostel/protocol"
)

// +case:id=carrier_forwarding_disabled,desc=`Run on a Linux carrier with ip_forward=0 and network tools present`,expect=`Auto reports the carrier forwarding gap and serves Beds; required rejects startup; neither changes the sysctl`
func TestCarrierForwardingDisabled(t *testing.T) {
	if os.Getenv("HOSTEL_E2E_REQUIRE_FORWARDING_DISABLED") != "1" {
		t.Skip("requires an explicit Linux forwarding-disabled profile")
	}
	binary := requireTestBinary(t)
	readForwarding := func() string {
		t.Helper()
		raw, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(raw))
	}
	if got := readForwarding(); got != "0" {
		t.Fatalf("profile requires ip_forward=0, got %q", got)
	}
	t.Cleanup(func() {
		if got := readForwarding(); got != "0" {
			t.Errorf("Hostel changed carrier forwarding: %q", got)
		}
	})
	for _, name := range []string{"ip", "nft"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("forwarding probe prerequisite %s: %v", name, err)
		}
	}
	for _, backend := range []string{"local", "supervisor"} {
		t.Run(backend, func(t *testing.T) {
			c := startTarget(t, targetOptions{executor: backend}).client
			var status struct {
				Isolation  protocol.RoomStatus `json:"isolation"`
				Subsystems struct {
					Filesystem struct {
						Effective string
						Room      protocol.RoomType
					}
					Network network.Status
				} `json:"subsystems"`
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			response, err := c.json(ctx, http.MethodGet, "/v1/status", "", nil, &status)
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "forwarding diagnostics", response)
			n := status.Subsystems.Network
			if n.Effective != network.Shared || n.Room != protocol.Dorm || n.Cause != "carrier" || n.Probe.Stage != "forwarding" || n.Tools["netns"].Selected || n.Tools["netns"].Probe != "unavailable" || !strings.Contains(n.Reason, "forwarding") || status.Isolation.Effective != protocol.Dorm {
				t.Fatalf("forwarding gap misreported: %+v", status)
			}
			t.Logf("ip_forward=0 filesystem=%s/%s network=%s/%s cause=%s stage=%s room=%s", status.Subsystems.Filesystem.Effective, status.Subsystems.Filesystem.Room, n.Effective, n.Room, n.Cause, n.Probe.Stage, status.Isolation.Effective)
			const name = "forwarding-disabled"
			response, err = c.json(ctx, http.MethodPost, "/v1/beds", "", map[string]any{"id": name, "networkPolicy": map[string]any{"defaultAction": "deny"}, "pillows": workspacePillows()}, nil)
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "declare forwarding-disabled Bed", response)
			c.waitBed(t, name, func(b bedView) bool { return b.Status.Readiness.Ready }, "ready despite carrier forwarding gap")
			var detail protocol.BedDetail
			response, err = c.json(ctx, http.MethodGet, "/v1/beds/"+name, "", nil, &detail)
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "Bed realized guarantees", response)
			b := detail.Status.Subsystems
			if detail.Status.Isolation.Effective != protocol.Dorm || b.Network.Cause != "carrier" || b.Network.PolicyStatus != "unenforced" || b.Network.RequestedPolicy == nil || b.Network.Policy != nil || b.Filesystem.Room != status.Subsystems.Filesystem.Room {
				t.Fatalf("Bed hid the gap or weakened filesystem: %+v", detail.Status)
			}
			result, response := c.command(t, name, map[string]any{"command": "printf forwarding-gap-reported", "timeout": 5000})
			must2xx(t, "execute with shared networking", response)
			assertCommandExit(t, result, 0)
			if result.Stdout != "forwarding-gap-reported" {
				t.Fatalf("stdout=%q", result.Stdout)
			}
		})
	}
	t.Run("required", func(t *testing.T) {
		options := config.Options{}
		options.Bed.Network.NetNS = value(tool.Required)
		raw, err := json.Marshal(options)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(t.TempDir(), "startup.json")
		if err := os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "--addr", fmt.Sprintf("127.0.0.1:%d", reservePort(t)), "--beds-root", filepath.Join(t.TempDir(), "beds"))
		cmd.Env = append(os.Environ(), "HOSTEL_E2E_CONFIG="+file)
		output, err := cmd.CombinedOutput()
		if ctx.Err() != nil || err == nil || !strings.Contains(string(output), "network.netns required") || !strings.Contains(string(output), "forwarding") {
			t.Fatalf("required forwarding failure: error=%v output=%s", err, output)
		}
	})
}
