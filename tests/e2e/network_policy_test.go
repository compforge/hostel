//go:build e2e

package e2e_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/go-stdx/shellx"
	"github.com/qiankunli/hostel/protocol"
)

func TestNetworkPolicyTraffic(t *testing.T) {
	if os.Getenv("HOSTEL_E2E_REQUIRE_NETWORK") != "1" {
		t.Skip("requires the disposable Linux network profile")
	}
	if os.Getenv(binaryEnv) == "" || os.Getenv(imageEnv) != "" {
		t.Fatal("network traffic probes require the binary profile")
	}
	for _, executor := range []string{"local", "supervisor"} {
		t.Run(executor, func(t *testing.T) { testNetworkPolicyTraffic(t, targetOptions{executor: executor}, false) })
	}
}

func testNetworkPolicyTraffic(t *testing.T, options targetOptions, shared bool) {
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			_ = connection.SetWriteDeadline(time.Now().Add(time.Second))
			_, _ = fmt.Fprintln(connection, "hostel-policy-fixture")
			_ = connection.Close()
		}
	}()
	c := startTarget(t, options).client
	deny := map[string]any{"defaultAction": "deny", "egress": []any{}}
	policyRequest(t, c, "POST", "/v1/beds", map[string]any{"id": "policy-a", "networkPolicy": deny, "pillows": workspacePillows()}, http.StatusAccepted)
	c.waitBed(t, "policy-a", func(b bedView) bool { return b.Status.Readiness.Ready }, "initial deny policy installed")
	must2xx(t, "create control Bed", c.createBed(t, "policy-b"))
	c.waitBed(t, "policy-b", func(b bedView) bool { return b.Status.Readiness.Ready }, "control Bed ready")
	if shared {
		var previousUID uint32
		for _, name := range []string{"policy-a", "policy-b"} {
			var detail protocol.BedDetail
			response, err := c.json(t.Context(), http.MethodGet, "/v1/beds/"+name, "", nil, &detail)
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "shared Bed detail", response)
			n, u := detail.Status.Subsystems.Network, detail.Status.Subsystems.Privilege
			if n.Level != "shared" || n.PolicyBackend != "uid" || !u.Assigned || u.UID == 0 || u.UID == previousUID {
				t.Fatalf("invalid UID realization: %+v", detail.Status)
			}
			previousUID = u.UID
			command, response := c.command(t, name, map[string]any{"command": "readlink /proc/self/ns/net", "timeout": 5000})
			must2xx(t, "shared namespace", response)
			assertCommandExit(t, command, 0)
			own, err := os.Readlink("/proc/self/ns/net")
			if err != nil || strings.TrimSpace(command.Stdout) != own {
				t.Fatalf("Bed unexpectedly changed netns: %q %s %v", command.Stdout, own, err)
			}
		}
	}
	ip := netip.MustParseAddr("127.0.0.1")
	var response httpResult
	if !shared {
		route, response := c.command(t, "policy-b", map[string]any{"command": "ip -4 route show default", "timeout": 5000})
		must2xx(t, "read Bed gateway", response)
		assertCommandExit(t, route, 0)
		fields := strings.Fields(route.Stdout)
		if len(fields) < 3 || fields[0] != "default" || fields[1] != "via" {
			t.Fatalf("invalid gateway route: %q", route.Stdout)
		}
		ip, err = netip.ParseAddr(fields[2])
		if err != nil {
			t.Fatal(err)
		}
	}
	address := net.JoinHostPort(ip.String(), fmt.Sprint(listener.Addr().(*net.TCPAddr).Port))
	// Both Beds reach this same listening pillow. A denial is meaningful only
	// after the control Bed proves the fixture and the carrier path are healthy.
	assertPolicyConnection(t, c, "policy-b", "", address, true)
	assertPolicyConnection(t, c, "policy-a", "", address, false)
	path := "/v1/beds/policy-a/network/policy"
	status := policyRequest(t, c, "GET", path, nil, http.StatusOK)
	if status.Mode != "deny_all" || status.Scope != "bed_processes" || status.Status != "ok" || status.EnforcementMode != "dns+nft" {
		t.Fatalf("initial policy: %+v", status)
	}
	var session struct {
		ID string `json:"session_id"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	response, err = c.json(ctx, "POST", "/session", "policy-a", map[string]string{}, &session)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "create policy session", response)
	if session.ID == "" {
		t.Fatal("session has no ID")
	}

	allowCIDR := []map[string]string{{"action": "allow", "target": ip.String() + "/32"}}
	policyRequest(t, c, "PATCH", path, allowCIDR, http.StatusOK)
	assertPolicyConnection(t, c, "policy-a", "", address, true)
	assertPolicyConnection(t, c, "policy-a", session.ID, address, true)
	// An overlapping deny wins even with an explicit allow. Live sessions must
	// observe updates without being recreated, while another Bed stays usable.
	policyRequest(t, c, "PATCH", path, []map[string]string{{"action": "deny", "target": ip.String()}}, http.StatusOK)
	assertPolicyConnection(t, c, "policy-a", "", address, false)
	assertPolicyConnection(t, c, "policy-a", session.ID, address, false)
	assertPolicyConnection(t, c, "policy-b", "", address, true)
	policyRequest(t, c, "DELETE", path, []string{ip.String()}, http.StatusOK)
	assertPolicyConnection(t, c, "policy-a", "", address, true)
	policyRequest(t, c, "DELETE", path, []string{ip.String() + "/32"}, http.StatusOK)
	assertPolicyConnection(t, c, "policy-a", "", address, false)

	policyRequest(t, c, "PUT", path, map[string]any{"defaultAction": "allow"}, http.StatusOK)
	assertPolicyConnection(t, c, "policy-a", session.ID, address, true)
	policyRequest(t, c, "PUT", path, map[string]any{"defaultAction": "invalid"}, http.StatusBadRequest)
	if status := policyRequest(t, c, "GET", path, nil, http.StatusOK); status.Mode != "allow_all" {
		t.Fatalf("invalid update changed policy: %+v", status)
	}
	assertPolicyConnection(t, c, "policy-a", "", address, true)
	policyRequest(t, c, "POST", path, deny, http.StatusOK)
	assertPolicyConnection(t, c, "policy-a", "", address, false)
	policyRequest(t, c, "DELETE", "/v1/beds/policy-a", nil, http.StatusOK)
	policyRequest(t, c, "GET", path, nil, http.StatusNotFound)
	must2xx(t, "recreate without old policy", c.createBed(t, "policy-a"))
	c.waitBed(t, "policy-a", func(b bedView) bool { return b.Status.Readiness.Ready }, "recreated with default policy")
	assertPolicyConnection(t, c, "policy-a", "", address, true)
	assertPolicyConnection(t, c, "policy-b", "", address, true)
}

type policyView struct {
	Status          string `json:"status"`
	EnforcementMode string `json:"enforcementMode"`
	Mode            string `json:"mode"`
	Scope           string `json:"scope"`
}

func policyRequest(t *testing.T, c *apiClient, method, path string, body any, want int) policyView {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var result policyView
	var destination any
	if strings.HasSuffix(path, "/network/policy") && want == http.StatusOK {
		destination = &result
	}
	response, err := c.json(ctx, method, path, "", body, destination)
	if err != nil || (response.Status != want && !(want == http.StatusAccepted && response.Status == http.StatusOK)) {
		t.Fatalf("%s %s: status=%d want=%d err=%v body=%s", method, path, response.Status, want, err, response.Body)
	}
	return result
}

func assertPolicyConnection(t *testing.T, c *apiClient, bed, session, address string, allowed bool) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	mode := "blocked"
	if allowed {
		mode = "allowed"
	}
	command := "E2E_NETWORK_PROBE=" + mode + " E2E_NETWORK_ADDRESS=" + shellx.Quote(address) + " " + shellx.Quote(binary) + " -test.run=^TestNetworkTrafficProbe$"
	request := map[string]any{"command": command, "timeout": 8000}
	var output commandResult
	var response httpResult
	if session == "" {
		output, response = c.command(t, bed, request)
	} else {
		output, response = c.stream(t, "/session/"+url.PathEscape(session)+"/run", bed, request)
	}
	must2xx(t, "network probe", response)
	assertCommandExit(t, output, 0)
	if !strings.Contains(output.Stdout, "network-"+mode) {
		t.Fatalf("%s network probe: stdout=%q stderr=%q", bed, output.Stdout, output.Stderr)
	}
}

// Re-exec the E2E runner inside the Bed, avoiding curl/Python dependencies and
// making refused connections or malformed replies fail instead of passing as deny.
func TestNetworkTrafficProbe(t *testing.T) {
	mode := os.Getenv("E2E_NETWORK_PROBE")
	if mode == "" {
		t.Skip("Bed subprocess helper")
	}
	connection, err := net.DialTimeout("tcp4", os.Getenv("E2E_NETWORK_ADDRESS"), time.Second)
	if mode == "blocked" {
		if err == nil {
			_ = connection.Close()
			t.Fatal("policy allowed a forbidden connection")
		}
		if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
			t.Fatalf("fixture connection failed for a non-policy reason: %v", err)
		}
	} else {
		if err != nil {
			t.Fatalf("allowed connection failed: %v", err)
		}
		defer connection.Close()
		_ = connection.SetReadDeadline(time.Now().Add(time.Second))
		line, err := bufio.NewReader(connection).ReadString('\n')
		if err != nil || line != "hostel-policy-fixture\n" {
			t.Fatalf("wrong fixture reply %q: %v", line, err)
		}
	}
	fmt.Println("network-" + mode)
}
