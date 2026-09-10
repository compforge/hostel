//go:build linux

package network

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Opt-in because this exercises real namespaces/firewall state. Run in a
// disposable container, not in the developer host's network namespace.
func TestLinuxNetwork(t *testing.T) {
	mode := os.Getenv("HOSTEL_NETWORK_TEST")
	if mode == "" {
		t.Skip("requires disposable Linux container; set HOSTEL_NETWORK_TEST=enabled or disabled")
	}
	m := New(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	t.Logf("report: %+v", m.Report())
	if mode == "disabled" {
		if m.Report().Enabled || m.Report().Reason == "" {
			t.Fatal("expected honest disabled verdict")
		}
		return
	}
	if !m.Report().Enabled {
		t.Fatalf("network unavailable: %+v", m.Report())
	}
	for _, id := range []string{"a", "b"} {
		if err := m.Acquire(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	var identities []string
	for _, id := range []string{"a", "b"} {
		cmd := exec.Command("readlink", "/proc/self/ns/net")
		if err := m.Wrap(id, cmd); err != nil {
			t.Fatal(err)
		}
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		identities = append(identities, string(out))
	}
	if identities[0] == identities[1] {
		t.Fatal("Beds share a netns")
	}
	// Bed-to-carrier connectivity survives namespace entry and privilege drop.
	listener, err := net.Listen("tcp4", net.JoinHostPort(m.Gateway("a"), "0"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		c, e := listener.Accept()
		if e == nil {
			_, _ = c.Write([]byte("hello"))
			_ = c.Close()
		}
	}()
	helper := exec.Command(os.Args[0], "-test.run=^TestNetworkSocketHelper$")
	helper.Env = append(os.Environ(), "HOSTEL_NETWORK_HELPER=client", "HOSTEL_NETWORK_ADDRESS="+listener.Addr().String())
	if err := m.Wrap("a", helper); err != nil {
		t.Fatal(err)
	}
	if out, err := helper.CombinedOutput(); err != nil || !strings.Contains(string(out), "hello") {
		t.Fatalf("gateway: %s %v", out, err)
	}
	// A listener in Bed B must not be reachable from Bed A.
	b := m.beds["b"].(*linuxEndpoint)
	address := net.JoinHostPort(b.address.String(), "18081")
	server := exec.Command(os.Args[0], "-test.run=^TestNetworkSocketHelper$")
	server.Env = append(os.Environ(), "HOSTEL_NETWORK_HELPER=server", "HOSTEL_NETWORK_ADDRESS="+address)
	if err := m.Wrap("b", server); err != nil {
		t.Fatal(err)
	}
	stdout, err := server.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Process.Kill(); _ = server.Wait() }()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("listener: %q %v", line, err)
	}
	client := exec.Command(os.Args[0], "-test.run=^TestNetworkSocketHelper$")
	client.Env = append(os.Environ(), "HOSTEL_NETWORK_HELPER=blocked", "HOSTEL_NETWORK_ADDRESS="+address)
	if err := m.Wrap("a", client); err != nil {
		t.Fatal(err)
	}
	if out, err := client.CombinedOutput(); err != nil {
		t.Fatalf("cross-Bed isolation: %s %v", out, err)
	}
}
func TestNetworkSocketHelper(t *testing.T) {
	mode := os.Getenv("HOSTEL_NETWORK_HELPER")
	if mode == "" {
		t.Skip("subprocess helper")
	}
	address := os.Getenv("HOSTEL_NETWORK_ADDRESS")
	if mode == "server" {
		l, err := net.Listen("tcp4", address)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		fmt.Println("ready")
		c, err := l.Accept()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.Write([]byte("hello"))
		_ = c.Close()
		return
	}
	c, err := net.DialTimeout("tcp4", address, time.Second)
	if mode == "blocked" {
		if err == nil {
			_ = c.Close()
			t.Fatal("cross-Bed connection succeeded")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 5)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(string(buf[:n]))
}
