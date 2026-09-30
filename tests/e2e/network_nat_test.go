//go:build e2e && linux

package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/go-stdx/randx"
	"github.com/qiankunli/go-stdx/shellx"
	"github.com/qiankunli/hostel/internal/bed/tool"
	"github.com/qiankunli/hostel/internal/config"
)

// The fixture has no route back to a Bed: successful replies require SNAT on
// the carrier. A gateway-local listener would never exercise the NAT hook.
func TestNetworkNAT(t *testing.T) {
	if os.Getenv("HOSTEL_E2E_REQUIRE_NETWORK") != "1" {
		t.Skip("requires the disposable Linux network profile")
	}
	if os.Getenv(binaryEnv) == "" || os.Getenv(imageEnv) != "" {
		t.Fatal("NAT traffic probes require the binary profile")
	}
	address, source := startNATFixture(t)
	checkNATSource(t, address, source)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, executor := range []string{"local", "supervisor"} {
		t.Run(executor, func(t *testing.T) {
			options := config.Options{}
			options.Bed.Network.NetNS = value(tool.Required)
			c := startTarget(t, targetOptions{executor: executor, isolation: "suite", config: &options}).client
			command := "E2E_NETWORK_PROBE=nat-client E2E_NETWORK_ADDRESS=" + shellx.Quote(address) +
				" E2E_NAT_SOURCE=" + shellx.Quote(source) + " " + shellx.Quote(binary) + " -test.run=^TestNetworkNATProbe$"
			for _, bed := range []string{"nat-a", "nat-b"} {
				result, response := c.command(t, bed, map[string]any{"command": command, "timeout": 8000})
				must2xx(t, "Bed NAT connection", response)
				assertCommandExit(t, result, 0)
				if !strings.Contains(result.Stdout, "nat-source="+source) {
					t.Fatalf("NAT source not confirmed: stdout=%q stderr=%q", result.Stdout, result.Stderr)
				}
			}
		})
	}
}

func startNATFixture(t *testing.T) (address, source string) {
	t.Helper()
	// Reserve a documentation subnet only after checking all carrier routes.
	// Its namespace has only the connected route, never a default or Bed route.
	subnet := netip.MustParsePrefix("192.0.2.0/30")
	var routes []struct{ Dst string }
	if err := json.Unmarshal(natIP(t, "-j", "-4", "route", "show", "table", "all"), &routes); err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		if route.Dst == "default" || route.Dst == "0.0.0.0/0" {
			continue
		}
		prefix, err := netip.ParsePrefix(route.Dst)
		if err != nil {
			ip, parseErr := netip.ParseAddr(route.Dst)
			if parseErr != nil {
				t.Fatalf("unrecognized fixture route %q", route.Dst)
			}
			prefix = netip.PrefixFrom(ip, 32)
		}
		if subnet.Overlaps(prefix) {
			t.Fatalf("NAT fixture subnet %s overlaps carrier route %s", subnet, prefix)
		}
	}
	name := "hnt" + randx.Hex(4)
	peer := "hnp" + randx.Hex(4)
	natIP(t, "netns", "add", name)
	t.Cleanup(func() { natIP(t, "netns", "del", name) })
	natIP(t, "link", "add", name, "type", "veth", "peer", "name", peer)
	t.Cleanup(func() { natIP(t, "link", "del", name) })
	for _, args := range [][]string{
		{"link", "set", peer, "netns", name},
		{"addr", "add", "192.0.2.1/30", "dev", name},
		{"link", "set", name, "up"},
		{"-n", name, "addr", "add", "192.0.2.2/30", "dev", peer},
		{"-n", name, "link", "set", peer, "up"},
		{"-n", name, "link", "set", "lo", "up"},
	} {
		natIP(t, args...)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	server := exec.CommandContext(ctx, "ip", "netns", "exec", name, binary, "-test.run=^TestNetworkNATProbe$")
	server.Env = append(os.Environ(), "E2E_NETWORK_PROBE=nat-server", "E2E_NETWORK_ADDRESS=192.0.2.2:0")
	server.Stderr = os.Stderr
	stdout, err := server.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = server.Wait() })
	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		ready <- strings.TrimSpace(line)
	}()
	select {
	case address = <-ready:
		if _, _, err := net.SplitHostPort(address); err != nil {
			t.Fatalf("NAT fixture did not start: %q", address)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("NAT fixture readiness timeout")
	}
	return address, "192.0.2.1"
}

func natIP(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ip", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("NAT fixture ip %v: %v: %s", args, err, out)
	}
	return out
}

func checkNATSource(t *testing.T, address, expected string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp4", address, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || line != "nat-source="+expected+"\n" {
		t.Fatalf("NAT response=%q expected source=%s err=%v", line, expected, err)
	}
	fmt.Print(line)
}

func TestNetworkNATProbe(t *testing.T) {
	switch os.Getenv("E2E_NETWORK_PROBE") {
	case "nat-client":
		checkNATSource(t, os.Getenv("E2E_NETWORK_ADDRESS"), os.Getenv("E2E_NAT_SOURCE"))
	case "nat-server":
		listener, err := net.Listen("tcp4", os.Getenv("E2E_NETWORK_ADDRESS"))
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		fmt.Println(listener.Addr())
		for {
			conn, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
			_, _ = fmt.Fprintf(conn, "nat-source=%s\n", conn.RemoteAddr().(*net.TCPAddr).IP)
			_ = conn.Close()
		}
	default:
		t.Skip("NAT fixture subprocess")
	}
}
