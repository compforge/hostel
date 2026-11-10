//go:build e2e && linux

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qiankunli/go-stdx/shellx"
	"github.com/qiankunli/hostel/internal/bed/privilege"
	"github.com/qiankunli/hostel/internal/bed/tool"
	"github.com/qiankunli/hostel/internal/config"
	"github.com/qiankunli/hostel/protocol"
)

func uidPolicyOptions(t *testing.T) config.Options {
	t.Helper()
	if os.Getenv("HOSTEL_E2E_REQUIRE_UID_POLICY") != "1" {
		t.Skip("requires disposable Linux carrier with UID+nft prerequisites")
	}
	requireTestBinary(t)
	raw, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil || strings.TrimSpace(string(raw)) != "0" {
		t.Fatalf("requires ip_forward=0: %q %v", raw, err)
	}
	options := restrictedOptions()
	options.Bed.Network.UIDNFT = value(tool.Required)
	options.Bed.Filesystem.UID = value(tool.Required)
	return options
}

func uidBedDetail(t *testing.T, c *apiClient, name string) protocol.BedDetail {
	t.Helper()
	var detail protocol.BedDetail
	response, err := c.json(t.Context(), http.MethodGet, "/v1/beds/"+name, "", nil, &detail)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "Bed identity and policy", response)
	u, n := detail.Status.Subsystems.Privilege, detail.Status.Subsystems.Network
	if !u.Assigned || u.UID < privilege.UIDMin || u.UID > privilege.UIDMax || u.GID != u.UID || !slices.Equal(u.SupplementaryGIDs, []int{1000}) || n.Isolation.Level != "shared" || n.Egress.Backend != "uid_nft" {
		t.Fatalf("unexpected identity/network realization: %+v", detail.Status.Subsystems)
	}
	return detail
}

// +case:id=bed_identity_crash_recovery,desc=`Crash a daemon with retained Bed data and UID egress, then admit another Bed before resuming`,expect=`Reserved UID and local identity survive; credentials, file ownership and redeclared egress remain effective`
// +link:doc=docs/bed.md
func TestBedIdentityCrashRecovery(t *testing.T) {
	options := uidPolicyOptions(t)
	t.Cleanup(func() {
		if t.Failed() {
			rules, err := exec.Command("nft", "list", "ruleset").CombinedOutput()
			t.Logf("run-owned carrier rules after recovery failure (err=%v):\n%s", err, rules)
		}
	})
	listener := policyListener(t)
	address := net.JoinHostPort("127.0.0.1", fmt.Sprint(listener.Addr().(*net.TCPAddr).Port))
	root, err := os.MkdirTemp("", "hostel-e2e-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(func() { assertNoNetworkRecords(t, root) })
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	const name = "retained"
	declaration := map[string]any{"id": name, "networkPolicy": map[string]any{"defaultAction": "deny"}, "pillows": workspacePillows()}
	var before protocol.BedDetail
	if !t.Run("before-crash", func(t *testing.T) {
		target := startTarget(t, targetOptions{executor: "supervisor", config: &options, bedsRoot: root})
		c := target.client
		policyRequest(t, c, "POST", "/v1/beds", declaration, http.StatusAccepted)
		c.waitBed(t, name, func(b bedView) bool { return b.Status.Readiness.Ready }, "initial Bed ready")
		before = uidBedDetail(t, c, name)
		result, response := c.command(t, name, map[string]any{"command": "printf retained-data > marker; chmod 600 marker", "timeout": 5000})
		must2xx(t, "write retained data", response)
		assertCommandExit(t, result, 0)
		must2xx(t, "declare control", c.createBed(t, "control"))
		assertPolicyConnection(t, c, "control", "", address, true)
		assertPolicyConnection(t, c, name, "", address, false)
		assertPersistedIdentity(t, root, name, before)
		target.crash()
		t.Logf("crashed daemon after Bed %s acquired UID %d", before.ID, before.Status.Subsystems.Privilege.UID)
	}) {
		return
	}
	t.Run("after-crash", func(t *testing.T) {
		c := startTarget(t, targetOptions{executor: "supervisor", config: &options, bedsRoot: root}).client
		// Recovery must reserve cold identities before any new allocation.
		must2xx(t, "admit newcomer before resume", c.createBed(t, "newcomer"))
		c.waitBed(t, "newcomer", func(b bedView) bool { return b.Status.Readiness.Ready }, "newcomer ready")
		other := uidBedDetail(t, c, "newcomer")
		if other.Status.Subsystems.Privilege.UID == before.Status.Subsystems.Privilege.UID {
			t.Fatal("new Bed reused a retained UID")
		}
		policyRequest(t, c, "POST", "/v1/beds", declaration, http.StatusAccepted)
		c.waitBed(t, name, func(b bedView) bool { return b.Status.Readiness.Ready }, "resumed Bed ready")
		after := uidBedDetail(t, c, name)
		if after.ID != before.ID || after.Status.Subsystems.Privilege.UID != before.Status.Subsystems.Privilege.UID {
			t.Fatalf("retained identity changed: before=%+v after=%+v", before, after)
		}
		assertPersistedIdentity(t, root, name, after)
		uid := after.Status.Subsystems.Privilege.UID
		command := fmt.Sprintf(`set -eu; test "$(cat marker)" = retained-data; test "$(id -u):$(id -g)" = %d:%d; test "$(stat -c '%%u:%%g' marker)" = %d:%d; awk '/^Groups:/ { if (NF != 2 || $2 != 1000) exit 1 }' /proc/self/status; printf resumed >> marker`, uid, uid, uid, uid)
		result, response := c.command(t, name, map[string]any{"command": command, "timeout": 5000})
		must2xx(t, "resumed process and private file", response)
		assertCommandExit(t, result, 0)
		waitRootFile(t, c, name, "/workspace/marker", "retained-dataresumed")
		result, response = c.command(t, "newcomer", map[string]any{"command": "cat " + shellx.Quote(filepath.Join(root, name, "volumes/workspace/marker")), "timeout": 5000})
		must2xx(t, "neighbor private file probe", response)
		if result.Result == nil || result.Result.Process.ExitCode == nil || *result.Result.Process.ExitCode == 0 || !strings.Contains(result.Stderr, "Permission denied") {
			t.Fatalf("neighbor denial was not file permission enforcement: %+v", result)
		}
		assertPolicyConnection(t, c, "newcomer", "", address, true)
		assertPolicyConnection(t, c, name, "", address, false)
		policyRequest(t, c, "PUT", "/v1/beds/"+name+"/network/policy", map[string]any{"defaultAction": "allow"}, http.StatusOK)
		assertPolicyConnection(t, c, name, "", address, true)
		policyRequest(t, c, "DELETE", "/v1/beds/"+name+"?purge=true", nil, http.StatusOK)
		if _, err := os.Stat(filepath.Join(root, ".identities", name+".local")); !os.IsNotExist(err) {
			t.Fatalf("purge retained identity record: %v", err)
		}
		must2xx(t, "recreate after purge", c.createBed(t, name))
		c.waitBed(t, name, func(b bedView) bool { return b.Status.Readiness.Ready }, "fresh Bed ready")
		fresh := uidBedDetail(t, c, name)
		if fresh.ID == before.ID {
			t.Fatal("purged Bed retained its local identity")
		}
		if response := c.download(t, name, "/workspace/marker"); response.Status != http.StatusNotFound {
			t.Fatalf("purged data returned: %+v", response)
		}
		assertPolicyConnection(t, c, name, "", address, true)
		t.Logf("restored Bed %s UID %d; newcomer UID %d; purge created %s", after.ID, uid, other.Status.Subsystems.Privilege.UID, fresh.ID)
	})
}

// +case:id=bed_uid_pillow_egress,desc=`Pillow main processes share GID 1000 while using independent UID egress`,expect=`Deny and allow apply before initial launch and after policy update plus Pillow restart`
// +link:doc=docs/network.md
func TestBedUIDPillowEgress(t *testing.T) {
	options := uidPolicyOptions(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"local", "supervisor"} {
		t.Run(backend, func(t *testing.T) {
			listener := policyListener(t)
			address := net.JoinHostPort("127.0.0.1", fmt.Sprint(listener.Addr().(*net.TCPAddr).Port))
			c := startTarget(t, targetOptions{executor: backend, config: &options}).client
			command := `set -eu; awk '/^Groups:/ { if (NF != 2 || $2 != 1000) exit 1 }' /proc/self/status; if test -f expected; then E2E_NETWORK_PROBE=$(cat expected); fi; export E2E_NETWORK_PROBE; ` + shellx.Quote(binary) + ` -test.run=^TestNetworkTrafficProbe$; id -u > uid; printf '%s' "$E2E_NETWORK_PROBE" > result; exec sleep 300`
			var previousUID uint32
			for _, name := range []string{"pillow-allow", "pillow-deny"} {
				mode, action := "allowed", "allow"
				if name == "pillow-deny" {
					mode, action = "blocked", "deny"
				}
				policyRequest(t, c, "POST", "/v1/beds", map[string]any{"id": name, "networkPolicy": map[string]any{"defaultAction": action}, "pillows": []map[string]any{{"name": "writer", "mounts": []map[string]string{{"volume": "workspace", "path": "/workspace"}}, "command": []string{"/bin/sh", "-c", command}, "env": map[string]string{"E2E_NETWORK_PROBE": mode, "E2E_NETWORK_ADDRESS": address}, "required": true, "restart_policy": "never", "stop_seconds": 1}}}, http.StatusAccepted)
				initial := waitVolumeWriter(t, c, name, "")
				waitRootFile(t, c, name, "/workspace/result", mode)
				detail := uidBedDetail(t, c, name)
				uid := detail.Status.Subsystems.Privilege.UID
				if uid == previousUID {
					t.Fatal("Pillows from distinct Beds share a UID")
				}
				previousUID = uid
				waitRootFile(t, c, name, "/workspace/uid", fmt.Sprintf("%d\n", uid))
				// Reverse each policy and verify a newly launched main process sees it.
				nextMode, nextAction := "blocked", "deny"
				if mode == "blocked" {
					nextMode, nextAction = "allowed", "allow"
				}
				must2xx(t, "set next probe expectation", c.upload(t, name, "/workspace/expected", []byte(nextMode)))
				policyRequest(t, c, "PUT", "/v1/beds/"+name+"/network/policy", map[string]any{"defaultAction": nextAction}, http.StatusOK)
				policyRequest(t, c, "POST", "/v1/beds/"+name+"/pillows/writer/process/restart", nil, http.StatusAccepted)
				waitVolumeWriter(t, c, name, initial)
				waitRootFile(t, c, name, "/workspace/result", nextMode)
				waitRootFile(t, c, name, "/workspace/uid", fmt.Sprintf("%d\n", uid))
				t.Logf("%s UID %d main process: %s -> %s after restart", name, uid, mode, nextMode)
			}
		})
	}
}

func assertNoNetworkRecords(t *testing.T, root string) {
	t.Helper()
	records, err := filepath.Glob(filepath.Join(root, ".network", "*.json"))
	if err != nil || len(records) != 0 {
		t.Errorf("network ownership remains after cleanup: %v err=%v", records, err)
	}
}

func assertPersistedIdentity(t *testing.T, root, name string, detail protocol.BedDetail) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".identities", name+".local"))
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		ID  string  `json:"id"`
		UID *uint32 `json:"uid"`
		GID *uint32 `json:"gid"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	user := detail.Status.Subsystems.Privilege
	if record.ID != string(detail.ID) || record.UID == nil || record.GID == nil || *record.UID != user.UID || *record.GID != user.GID {
		t.Fatalf("persisted identity differs from running Bed: record=%s bed=%+v", data, detail)
	}
}
