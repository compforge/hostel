//go:build e2e

package e2e_test

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/tool"
)

func TestRootfulNetworkResolver(t *testing.T) {
	if os.Getenv("HOSTEL_E2E_REQUIRE_NETWORK") != "1" || os.Getenv("HOSTEL_E2E_REQUIRE_ROOTFUL") != "1" {
		t.Skip("requires explicit rootful and network profiles")
	}
	requireTestBinary(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	_, err := net.DefaultResolver.LookupHost(ctx, "example.com")
	cancel()
	if err != nil {
		t.Fatalf("runner DNS prerequisite: %v", err)
	}
	for _, backend := range []string{"local", "supervisor"} {
		t.Run(backend, func(t *testing.T) {
			options := restrictedOptions()
			options.Bed.Filesystem.Bwrap = value(tool.Required)
			options.Bed.Network.NetNS = value(tool.Required)
			c := startTarget(t, targetOptions{isolation: "suite", executor: backend, config: &options}).client
			const bed = "resolver"
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			response, err := c.json(ctx, "POST", "/v1/beds", "", map[string]any{"id": bed,
				"services": []map[string]any{{"name": "dns", "command": []string{"/bin/sh", "-c", "set -eu; cat /etc/resolv.conf > /workspace/service-resolver; getent ahostsv4 example.com > /workspace/service-dns; exec sleep 300"}, "required": true, "restart": "never", "stop_seconds": 1}}}, nil)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "create network Bed", response)
			c.waitBed(t, bed, func(b bedView) bool { return b.Status.Readiness.Ready }, "network Service ready")
			var detail struct {
				Status struct {
					Network struct {
						Gateway string `json:"gateway"`
					} `json:"network"`
				} `json:"status"`
			}
			ctx, cancel = context.WithTimeout(t.Context(), 5*time.Second)
			response, err = c.json(ctx, "GET", "/v1/beds/"+bed, "", nil, &detail)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "Bed network status", response)
			if net.ParseIP(detail.Status.Network.Gateway) == nil {
				t.Fatal("missing Bed gateway")
			}
			result, response := c.command(t, bed, map[string]any{"command": "set -eu; for i in 1 2 3 4 5; do test -s /workspace/service-dns && break; sleep 1; done; cmp /etc/resolv.conf /workspace/service-resolver; test -s /workspace/service-dns; getent ahostsv4 example.com >/dev/null; cat /etc/resolv.conf", "timeout": 15000})
			must2xx(t, "Bed DNS query", response)
			assertCommandExit(t, result, 0)
			if !strings.Contains(result.Stdout, "nameserver "+detail.Status.Network.Gateway+"\n") {
				t.Fatalf("wrong resolver: %q", result.Stdout)
			}
		})
	}
}
