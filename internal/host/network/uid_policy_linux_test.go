//go:build linux

package network

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestUIDRulesScopeDNSAndDoNotBypassPolicy(t *testing.T) {
	rules := renderUIDPolicy("owned", 200001, 0, "127.0.0.1", 40531, Policy{DefaultAction: "deny", Egress: []Rule{{Action: "allow", Target: "10.0.0.0/8"}, {Action: "deny", Target: "10.1.0.0/16"}}})
	for _, want := range []string{"meta skuid 200001 jump workload", "meta skuid 200001 udp dport 53 dnat to 127.0.0.1:40531", "meta skuid != 200001 meta skuid != 0 drop", "meta nfproto ipv6 drop"} {
		if !strings.Contains(rules, want) {
			t.Fatalf("missing %q in %s", want, rules)
		}
	}
	if strings.Contains(rules, "oifname lo accept") || strings.Contains(rules, "ct state established") || strings.Index(rules, "10.1.0.0/16 drop") > strings.Index(rules, "10.0.0.0/8 accept") {
		t.Fatal(rules)
	}
}

// +case:id=uid_egress_no_forwarding,desc=`Distinct socket UIDs in one netns`,expect=`DNS and TCP/UDP rules stay isolated; IPv6 and direct cross-resolver access cannot bypass; revocation and UID reuse are safe`
func TestUIDPolicyTraffic(t *testing.T) {
	if os.Getenv("HOSTEL_UID_POLICY_TEST") != "enabled" {
		t.Skip("requires disposable Linux carrier with NET_ADMIN and distinct UIDs")
	}
	if raw, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward"); err != nil || strings.TrimSpace(string(raw)) != "0" {
		t.Fatalf("requires forwarding disabled: %q %v", raw, err)
	}
	provider, probe := ProbeUIDPolicy(t.Context(), nil)
	if provider == nil {
		t.Fatalf("probe=%+v", probe)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := provider.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	backend := provider.(*uidPolicyBackend)
	dns := startUIDDNSFixture(t)
	backend.upstreams = []string{dns}
	a, err := provider.Acquire(t.Context(), "test-bed", 200001)
	if err != nil {
		t.Fatal(err)
	}
	b, err := provider.Acquire(t.Context(), "test-bed", 200002)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Acquire(t.Context(), "test-bed", 200001); err == nil {
		t.Fatal("duplicate UID acquired")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	go func() {
		buf := make([]byte, 64)
		for {
			n, addr, err := udp.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = udp.WriteTo(buf[:n], addr)
		}
	}()
	change := func(lease PolicyLease, p Policy) {
		t.Helper()
		s, err := lease.Update(t.Context(), PolicyMutation{Replace: &p})
		if err != nil || s.Status != "ok" {
			t.Fatalf("policy=%+v %v", s, err)
		}
	}
	allow := Policy{DefaultAction: "allow"}
	deny := Policy{DefaultAction: "deny"}
	change(a, deny)
	for _, dest := range []struct{ protocol, address string }{{"tcp4", listener.Addr().String()}, {"udp4", udp.LocalAddr().String()}} {
		uidProbe(t, 200002, dest.protocol, dest.address, "allow")
		uidProbe(t, 200001, dest.protocol, dest.address, "drop")
	}
	// DNS traverses IPv4 OUTPUT NAT even when the requested resolver is remote.
	query := net.JoinHostPort("192.0.2.53", "53")
	for _, transport := range []string{"dns-udp", "dns-tcp"} {
		uidProbe(t, 200002, transport, query, "allow")
		uidProbe(t, 200001, transport, query, "refuse")
	}
	change(a, Policy{DefaultAction: "deny", Egress: []Rule{{Action: "allow", Target: "allowed.test"}}})
	uidProbe(t, 200001, "dns-udp", query, "allow")
	uidProbe(t, 200001, "tcp4", listener.Addr().String(), "allow")
	// Learning an address for A never opens it for B, and querying A's listener
	// directly is denied even under B's default-allow policy.
	uidProbe(t, 200002, "dns-udp", a.(*uidPolicyLease).dns.address, "drop")
	change(b, deny)
	uidProbe(t, 200002, "tcp4", listener.Addr().String(), "drop")
	assertUIDEstablishedRevoked(t, a, listener.Addr().String())
	change(a, deny)
	uidProbe(t, 200001, "tcp4", listener.Addr().String(), "drop")
	// An invalid update must leave the installed deny policy intact.
	if _, err := a.Update(t.Context(), PolicyMutation{Replace: &Policy{DefaultAction: "invalid"}}); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatal(err)
	}
	uidProbe(t, 200001, "tcp4", listener.Addr().String(), "drop")
	change(a, allow)
	ipv6, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ipv6.Close()
	uidProbe(t, 200001, "tcp6", ipv6.Addr().String(), "drop")
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	replacement, err := provider.Acquire(t.Context(), "test-bed", 200001)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	change(replacement, allow)
	uidProbe(t, 200001, "tcp4", listener.Addr().String(), "allow")
	uidProbe(t, 200002, "tcp4", listener.Addr().String(), "drop")
}

func uidProbe(t *testing.T, uid uint32, protocol, address, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestUIDPolicyProbe$")
	cmd.Env = append(os.Environ(), "HOSTEL_UID_PROBE="+protocol, "HOSTEL_UID_ADDRESS="+address, "HOSTEL_UID_EXPECT="+want)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid}}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("uid=%d %s %s want=%s: %v %s", uid, protocol, address, want, err, out)
	}
}
func TestUIDPolicyProbe(t *testing.T) {
	protocol := os.Getenv("HOSTEL_UID_PROBE")
	if protocol == "" {
		t.Skip("subprocess helper")
	}
	address, want := os.Getenv("HOSTEL_UID_ADDRESS"), os.Getenv("HOSTEL_UID_EXPECT")
	ctx, cancel := context.WithTimeout(t.Context(), 800*time.Millisecond)
	defer cancel()
	var err error
	if protocol == "flow" {
		conn, err := net.DialTimeout("tcp4", address, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := conn.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			t.Fatal(err)
		}
		fmt.Println("connected")
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
		_, err = conn.Write([]byte("ping"))
		if err == nil {
			_, err = io.ReadFull(conn, buf)
		}
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("existing flow was not revoked: %v", err)
		}
		return
	}
	if strings.HasPrefix(protocol, "dns-") {
		question := dnsmessage.Message{Header: dnsmessage.Header{ID: 42, RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: dnsmessage.MustNewName("allowed.test."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}
		packet, _ := question.Pack()
		var response []byte
		response, err = dnsExchange(ctx, strings.TrimPrefix(protocol, "dns-"), address, packet)
		if err == nil {
			var answer dnsmessage.Message
			if err := answer.Unpack(response); err != nil {
				t.Fatal(err)
			}
			if want == "refuse" && answer.Header.RCode == dnsmessage.RCodeRefused {
				return
			}
			if want != "allow" || answer.Header.RCode != dnsmessage.RCodeSuccess || len(answer.Answers) != 1 {
				t.Fatalf("unexpected DNS response: %+v", answer)
			}
			return
		}
	} else {
		var conn net.Conn
		conn, err = (&net.Dialer{}).DialContext(ctx, protocol, address)
		if err == nil {
			defer conn.Close()
			if strings.HasPrefix(protocol, "udp") {
				_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
				_, err = conn.Write([]byte("ping"))
				if err == nil {
					buf := make([]byte, 4)
					_, err = conn.Read(buf)
				}
			}
		}
	}
	if want == "allow" {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	if want != "drop" || err == nil {
		t.Fatalf("wanted %s, got %v", want, err)
	}
	// Linux reports an OUTPUT drop synchronously as EPERM for UDP writes.
	if strings.Contains(protocol, "udp") && errors.Is(err, syscall.EPERM) {
		return
	}
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("not a policy drop: %v", err)
	}
}
func startUIDDNSFixture(t *testing.T) string {
	t.Helper()
	u, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp4", u.LocalAddr().String())
	if err != nil {
		u.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { u.Close(); l.Close() })
	answer := func(raw []byte) []byte {
		var m dnsmessage.Message
		if m.Unpack(raw) != nil {
			return nil
		}
		m.Header.Response = true
		m.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: m.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 30}, Body: &dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}}}}
		out, _ := m.Pack()
		return out
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, peer, err := u.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = u.WriteToUDP(answer(buf[:n]), peer)
		}
	}()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(time.Second))
				var size [2]byte
				if _, err := io.ReadFull(c, size[:]); err != nil {
					return
				}
				raw := make([]byte, binary.BigEndian.Uint16(size[:]))
				if _, err := io.ReadFull(c, raw); err != nil {
					return
				}
				out := answer(raw)
				binary.BigEndian.PutUint16(size[:], uint16(len(out)))
				_, _ = c.Write(append(size[:], out...))
			}()
		}
	}()
	return u.LocalAddr().String()
}

func assertUIDEstablishedRevoked(t *testing.T, lease PolicyLease, address string) {
	t.Helper()
	if _, err := lease.Update(t.Context(), PolicyMutation{Replace: &Policy{DefaultAction: "allow"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestUIDPolicyProbe$")
	cmd.Env = append(os.Environ(), "HOSTEL_UID_PROBE=flow", "HOSTEL_UID_ADDRESS="+address)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 200001, Gid: 200001}}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "connected\n" {
		t.Fatalf("flow handshake %q %v %s", line, err, stderr.String())
	}
	if _, err := lease.Update(t.Context(), PolicyMutation{Replace: &Policy{DefaultAction: "deny"}}); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(in, "recheck\n")
	if err := cmd.Wait(); err != nil {
		t.Fatalf("revocation: %v %s", err, stderr.String())
	}
}
