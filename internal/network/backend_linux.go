//go:build linux

package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/qiankunli/go-stdx/randx"
)

type linuxBackend struct {
	ip, nft, setpriv string
	prefix           string
	resolvers        []string
	resolverOptions  string
	endpoints        map[string]*linuxEndpoint
}

type linuxEndpoint struct {
	owner                            *linuxBackend
	name, link, peer                 string
	gateway, address                 netip.Addr
	dns                              *dnsForwarder
	policy                           *policyControl
	namespace, device, table, config bool
}

func run(ctx context.Context, input, command string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if len(detail) > 1024 {
			detail = detail[:1024]
		}
		return nil, fmt.Errorf("%s %s: %w: %s", filepath.Base(command), strings.Join(args, " "), err, detail)
	}
	return out, nil
}

func probeBackend(ctx context.Context) (backend, Probe) {
	probe := Probe{Stage: "tools"}
	b := &linuxBackend{prefix: "hn" + randx.Hex(4), endpoints: make(map[string]*linuxEndpoint)}
	var err error
	for _, tool := range []struct {
		name string
		path *string
	}{{"ip", &b.ip}, {"nft", &b.nft}, {"setpriv", &b.setpriv}} {
		*tool.path, err = exec.LookPath(tool.name)
		if err != nil {
			probe.Error = err.Error()
			return nil, probe
		}
	}
	probe.Stage = "forwarding"
	forwarding, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil || strings.TrimSpace(string(forwarding)) != "1" {
		probe.Error = "IPv4 forwarding is unavailable; Hostel does not change carrier-wide sysctls"
		return nil, probe
	}
	probe.Stage = "dns"
	b.resolvers, b.resolverOptions, err = readResolvers()
	if err != nil {
		probe.Error = err.Error()
		return nil, probe
	}
	probe.Attempted = true
	probe.Stage = "namespace_network"
	ep, err := b.Create(ctx)
	if err == nil {
		probe.Stage = "execution"
		// Exercise the SAME namespace entry and capability drop as Bed executions.
		cmd := exec.Command(b.setpriv, "--bounding-set=-all", "--inh-caps=-all", "--ambient-caps=-all", "--no-new-privs", "--", "/bin/sh", "-c", "readlink /proc/self/ns/net; cat /proc/self/status")
		ep.Wrap(cmd)
		out, e := run(ctx, "", cmd.Path, cmd.Args[1:]...)
		err = e
		if err == nil {
			own, _ := os.Readlink("/proc/self/ns/net")
			if strings.HasPrefix(string(out), own+"\n") || !strings.Contains(string(out), "CapEff:\t0000000000000000") {
				err = errors.New("namespace entry or capability drop was not effective")
			}
		}
	}

	if err == nil {
		probe.Stage = "connectivity"
		err = probeConnectivity(ctx, ep)
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	cleanupErr := b.Close(cleanupCtx)
	cancel()
	err = errors.Join(err, cleanupErr)
	if err != nil {
		probe.Error = err.Error()
		return nil, probe
	}
	probe.Stage = "ready"
	return b, probe
}

func (b *linuxBackend) Create(ctx context.Context) (endpoint, error) {
	gateway, address, err := b.allocate(ctx)
	if err != nil {
		return nil, err
	}
	suffix := randx.Hex(2)
	ep := &linuxEndpoint{owner: b, name: b.prefix + suffix, link: b.prefix + suffix, peer: "np" + randx.Hex(4), gateway: gateway, address: address}
	b.endpoints[ep.name] = ep
	if err = ep.create(ctx); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cleanupErr := ep.Close(cleanupCtx)
		cancel()
		return nil, errors.Join(err, cleanupErr)
	}
	return ep, nil
}

// Allocate only unused connected /30s. Never replace an existing host route.
// 198.18/15 is benchmark space, not an assumed globally free private subnet.
func (b *linuxBackend) allocate(ctx context.Context) (netip.Addr, netip.Addr, error) {
	out, err := run(ctx, "", b.ip, "-j", "-4", "route", "show", "table", "all")
	if err != nil {
		return netip.Addr{}, netip.Addr{}, err
	}
	var routes []struct {
		Dst string `json:"dst"`
	}
	if err = json.Unmarshal(out, &routes); err != nil {
		return netip.Addr{}, netip.Addr{}, err
	}
	for n := uint32(0); n < 32768; n++ {
		raw := uint32(198)<<24 | uint32(18)<<16 | n<<2
		bytes := [4]byte{byte(raw >> 24), byte(raw >> 16), byte(raw >> 8), byte(raw)}
		subnet := netip.PrefixFrom(netip.AddrFrom4(bytes), 30)
		busy := false
		for _, route := range routes {
			if route.Dst == "default" || route.Dst == "0.0.0.0/0" {
				continue
			}
			p, e := netip.ParsePrefix(route.Dst)
			if e != nil {
				if addr, e := netip.ParseAddr(route.Dst); e == nil {
					p = netip.PrefixFrom(addr, 32)
				}
			}
			if p.IsValid() && subnet.Overlaps(p) {
				busy = true
				break
			}
		}
		if !busy {
			return subnet.Addr().Next(), subnet.Addr().Next().Next(), nil
		}
	}
	return netip.Addr{}, netip.Addr{}, errors.New("no unused /30 network in 198.18.0.0/15")
}

func (e *linuxEndpoint) create(ctx context.Context) error {
	b := e.owner
	if _, err := run(ctx, "", b.ip, "netns", "add", e.name); err != nil {
		return err
	}
	e.namespace = true
	// ip netns exec gives every process a private resolver view, including suite.
	dir := filepath.Join("/etc/netns", e.name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	e.config = true
	e.dns = newDNSForwarder(e.gateway.String(), b.resolvers)
	e.policy = &policyControl{current: Policy{DefaultAction: "allow"}, apply: e.applyPolicy, learn: e.learnDNS}
	e.dns.policy = e.policy
	if err := os.WriteFile(filepath.Join(dir, "resolv.conf"), []byte("nameserver "+e.gateway.String()+"\n"+b.resolverOptions), 0644); err != nil {
		return err
	}
	if _, err := run(ctx, "", b.ip, "link", "add", e.link, "type", "veth", "peer", "name", e.peer); err != nil {
		return err
	}
	e.device = true
	for _, args := range [][]string{
		{"link", "set", e.peer, "netns", e.name},
		{"addr", "add", e.gateway.String() + "/30", "dev", e.link},
		{"link", "set", e.link, "up"},
		{"-n", e.name, "addr", "add", e.address.String() + "/30", "dev", e.peer},
		{"-n", e.name, "link", "set", e.peer, "up"},
		{"-n", e.name, "link", "set", "lo", "up"},
		{"-n", e.name, "route", "add", "default", "via", e.gateway.String()},
	} {
		if _, err := run(ctx, "", b.ip, args...); err != nil {
			return err
		}
	}
	if _, err := run(ctx, e.rules(), b.nft, "-f", "-"); err != nil {
		return err
	}
	e.table = true
	if err := e.applyPolicy(ctx, e.policy.current); err != nil {
		return err
	}
	if err := e.dns.Start(); err != nil {
		return err
	}
	return nil
}

// Scope every host rule to this veth. Existing CNI/firewall policy remains in
// force; this table cannot override a drop in another base chain.
func (e *linuxEndpoint) rules() string {
	return fmt.Sprintf(`table inet %s {
 chain forward { type filter hook forward priority -10; policy accept;
  iifname "%s" meta nfproto ipv6 drop
  oifname "%s" meta nfproto ipv6 drop
  iifname "%s" ip saddr != %s drop
  iifname "%s" ip daddr 198.18.0.0/15 drop
  oifname "%s" ct state established,related accept
  oifname "%s" drop
 }
 chain nat { type nat hook postrouting priority srcnat; policy accept;
  ip saddr %s oifname != "%s" masquerade
 }
}
`, e.name, e.link, e.link, e.link, e.address, e.link, e.link, e.link, e.address, e.link)
}

func (e *linuxEndpoint) Wrap(cmd *exec.Cmd) {
	// Enter before filesystem isolation. The complete execution environment
	// performs identity changes and final privilege dropping inside this wrapper.
	args := []string{e.owner.ip, "netns", "exec", e.name, cmd.Path}
	cmd.Args = append(args, cmd.Args[1:]...)
	cmd.Path = e.owner.ip
}
func (e *linuxEndpoint) Gateway() string { return e.gateway.String() }

func (e *linuxEndpoint) Close(ctx context.Context) error {
	var err error
	if e.policy != nil {
		e.policy.mu.Lock()
		e.policy.closed = true
		e.policy.mu.Unlock()
	}
	if e.dns != nil {
		e.dns.Close()
	}
	if e.device {
		if _, x := run(ctx, "", e.owner.ip, "link", "del", e.link); x != nil {
			err = errors.Join(err, x)
		} else {
			e.device = false
		}
	}
	if e.table {
		if _, x := run(ctx, "", e.owner.nft, "delete", "table", "inet", e.name); x != nil {
			err = errors.Join(err, x)
		} else {
			e.table = false
		}
	}
	if e.namespace {
		if _, x := run(ctx, "", e.owner.ip, "netns", "del", e.name); x != nil {
			err = errors.Join(err, x)
		} else {
			e.namespace = false
		}
	}
	if e.config {
		if x := os.RemoveAll(filepath.Join("/etc/netns", e.name)); x != nil {
			err = errors.Join(err, x)
		} else {
			e.config = false
		}
	}
	if err == nil {
		delete(e.owner.endpoints, e.name)
	}
	return err
}
func (b *linuxBackend) Close(ctx context.Context) error {
	var err error
	for _, e := range b.endpoints {
		err = errors.Join(err, e.Close(ctx))
	}
	return err
}

func readResolvers() ([]string, string, error) {
	data, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil, "", err
	}
	var resolvers []string
	var options strings.Builder
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) >= 2 && fields[0] == "nameserver" {
			if ip, err := netip.ParseAddr(fields[1]); err == nil {
				resolvers = append(resolvers, net.JoinHostPort(ip.String(), "53"))
			}
		} else if fields[0] == "search" || fields[0] == "domain" || fields[0] == "options" {
			options.WriteString(line + "\n")
		}
	}
	if len(resolvers) == 0 {
		return nil, "", errors.New("no nameservers in /etc/resolv.conf")
	}
	return resolvers, options.String(), nil
}

func probeConnectivity(ctx context.Context, ep endpoint) error {
	listener, err := net.Listen("tcp4", net.JoinHostPort(ep.Gateway(), "0"))
	if err != nil {
		return err
	}
	defer listener.Close()
	served := make(chan error, 1)
	go func() {
		c, e := listener.Accept()
		if e == nil {
			_ = c.SetDeadline(time.Now().Add(time.Second))
			_, e = c.Write([]byte("hostel-network\n"))
			_ = c.Close()
		}
		served <- e
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	// Only generated IP/port values enter this shell; no Bed or API input does.
	cmd := exec.Command("/bin/bash", "-c", fmt.Sprintf("exec 3<>/dev/tcp/%s/%d; IFS= read -r result <&3; test \"$result\" = hostel-network", ep.Gateway(), port))
	ep.Wrap(cmd)
	_, err = run(ctx, "", cmd.Path, cmd.Args[1:]...)
	_ = listener.Close()
	return errors.Join(err, <-served)
}
