//go:build e2e && linux

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/tool"
	"github.com/qiankunli/hostel/protocol"
)

// +case:id=private_netns_crash_recovery,desc=`Crash a daemon and resume a Bed on a fresh private netns`,expect=`Old egress rules do not block policy updates in the new namespace; record orphan network resources separately`
// +link:doc=docs/network.md
func TestPrivateNetworkCrashRecovery(t *testing.T) {
	if os.Getenv("HOSTEL_E2E_REQUIRE_NETWORK") != "1" {
		t.Skip("requires disposable privileged Linux network profile")
	}
	requireTestBinary(t)
	options := restrictedOptions()
	options.Bed.Network.NetNS = value(tool.Required)
	options.Bed.Filesystem.UID = value(tool.Required)
	options.Bed.Privilege.SupplementaryGIDs = value([]int{1000})
	listener := policyListener(t)
	root, err := os.MkdirTemp("", "hostel-e2e-netns-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { assertNoNetworkRecords(t, root) })
	// This is a run-owned carrier. Evidence collection never removes namespaces
	// from an arbitrary host; the disposable carrier owns final reclamation.
	t.Cleanup(func() {
		for _, args := range [][]string{{"ip", "netns", "list"}, {"nft", "list", "tables"}} {
			out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != "" {
				t.Errorf("network resources leaked: command=%v err=%v output=%s", args, err, out)
			}
			t.Logf("residual resources after both daemon fixtures close: %v err=%v\n%s", args, err, out)
		}
	})
	const name = "private-recovery"
	declaration := map[string]any{"id": name, "networkPolicy": map[string]any{"defaultAction": "deny"}, "pillows": workspacePillows()}
	const sentinel = "hostel_recovery_foreign"
	if out, err := exec.Command("nft", "add", "table", "inet", sentinel).CombinedOutput(); err != nil {
		t.Fatalf("foreign table fixture: %v %s", err, out)
	}
	t.Cleanup(func() {
		out, err := exec.Command("nft", "-j", "list", "table", "inet", sentinel).CombinedOutput()
		var tables any
		if err != nil || json.Unmarshal(out, &tables) != nil {
			t.Errorf("recovery removed unrelated table: %v %s", err, out)
		}
		if out, err := exec.Command("nft", "delete", "table", "inet", sentinel).CombinedOutput(); err != nil {
			t.Errorf("foreign table fixture cleanup: %v %s", err, out)
		}
	})
	var before protocol.BedDetail
	var oldNamespace string
	observe := func(t *testing.T, c *apiClient) (protocol.BedDetail, string, string) {
		t.Helper()
		var detail protocol.BedDetail
		response, err := c.json(t.Context(), "GET", "/v1/beds/"+name, "", nil, &detail)
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "private network detail", response)
		n := detail.Status.Subsystems.Network
		if n.Level != "private" || n.Gateway == "" {
			t.Fatalf("private network unavailable: %+v", n)
		}
		result, response := c.command(t, name, map[string]any{"command": "readlink /proc/self/ns/net", "timeout": 5000})
		must2xx(t, "observe Bed netns", response)
		assertCommandExit(t, result, 0)
		namespace := strings.TrimSpace(result.Stdout)
		if namespace == "" {
			t.Fatal("missing namespace identity")
		}
		return detail, namespace, net.JoinHostPort(n.Gateway, fmt.Sprint(listener.Addr().(*net.TCPAddr).Port))
	}
	if !t.Run("before-crash", func(t *testing.T) {
		target := startTarget(t, targetOptions{executor: "supervisor", config: &options, bedsRoot: root})
		c := target.client
		policyRequest(t, c, "POST", "/v1/beds", declaration, http.StatusAccepted)
		c.waitBed(t, name, func(b bedView) bool { return b.Status.Readiness.Ready }, "private Bed ready")
		var address string
		before, oldNamespace, address = observe(t, c)
		policyRequest(t, c, "PUT", "/v1/beds/"+name+"/network/policy", map[string]any{"defaultAction": "allow"}, http.StatusOK)
		assertPolicyConnection(t, c, name, "", address, true)
		policyRequest(t, c, "PUT", "/v1/beds/"+name+"/network/policy", map[string]any{"defaultAction": "deny"}, http.StatusOK)
		assertPolicyConnection(t, c, name, "", address, false)
		target.crash()
	}) {
		return
	}
	t.Run("after-crash", func(t *testing.T) {
		c := startTarget(t, targetOptions{executor: "supervisor", config: &options, bedsRoot: root}).client
		policyRequest(t, c, "POST", "/v1/beds", declaration, http.StatusAccepted)
		c.waitBed(t, name, func(b bedView) bool { return b.Status.Readiness.Ready }, "private Bed resumed")
		after, namespace, address := observe(t, c)
		if after.ID != before.ID || after.Status.Subsystems.Privilege.UID != before.Status.Subsystems.Privilege.UID || namespace == oldNamespace {
			t.Fatalf("invalid recovery: old ID=%s UID=%d netns=%s; new ID=%s UID=%d netns=%s", before.ID, before.Status.Subsystems.Privilege.UID, oldNamespace, after.ID, after.Status.Subsystems.Privilege.UID, namespace)
		}
		assertPolicyConnection(t, c, name, "", address, false)
		policyRequest(t, c, "PUT", "/v1/beds/"+name+"/network/policy", map[string]any{"defaultAction": "allow"}, http.StatusOK)
		assertPolicyConnection(t, c, name, "", address, true)
		t.Logf("retained Bed %s UID %d changed namespace %s -> %s and gateway %s -> %s; policy update passed", after.ID, after.Status.Subsystems.Privilege.UID, oldNamespace, namespace, before.Status.Subsystems.Network.Gateway, after.Status.Subsystems.Network.Gateway)
	})
}
