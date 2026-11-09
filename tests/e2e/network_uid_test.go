//go:build e2e && linux

package e2e_test

import (
	"github.com/qiankunli/hostel/internal/bed/tool"
	"github.com/qiankunli/hostel/internal/config"
	"os"
	"strings"
	"testing"
)

// +case:id=bed_uid_egress,desc=`Carrier forwarding disabled, distinct Bed UIDs`,expect=`Shared network enforces BedSpec and live policy updates through the public API for local and supervisor execution`
func TestSharedUIDNetworkPolicyTraffic(t *testing.T) {
	if os.Getenv("HOSTEL_E2E_REQUIRE_UID_POLICY") != "1" {
		t.Skip("requires disposable Linux carrier with UID+nft prerequisites")
	}
	requireTestBinary(t)
	raw, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil || strings.TrimSpace(string(raw)) != "0" {
		t.Fatalf("requires ip_forward=0: %q %v", raw, err)
	}
	for _, executor := range []string{"local", "supervisor"} {
		t.Run(executor, func(t *testing.T) {
			options := config.Options{}
			options.Bed.Privilege.SupplementaryGIDs = value([]int{1000})
			options.Bed.Network.NetNS = value(tool.Auto)
			testNetworkPolicyTraffic(t, targetOptions{executor: executor, config: &options}, true)
		})
	}
	after, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil || string(after) != string(raw) {
		t.Fatalf("forwarding changed: %q %v", after, err)
	}
}
