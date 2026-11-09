//go:build linux

package network

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

func (e *linuxEndpoint) Policy() *policyControl { return e.policy }
func (e *linuxEndpoint) applyPolicy(ctx context.Context, p Policy) error {
	_, err := run(ctx, renderPolicy(p, e.gateway.String()), e.owner.ip, "netns", "exec", e.name, e.owner.nft, "-f", "-")
	return policyTransactionError(ctx, err)
}

// +spec=`A normal nft rejection leaves the old transaction intact; cancellation or a killed process leaves commit acknowledgement uncertain.`
func policyTransactionError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if ctx.Err() == nil && errors.As(err, &exit) && exit.ProcessState.Exited() {
		return &PolicyUnchangedError{Err: err}
	}
	return err
}

// Delete/create is one nft transaction: readers never observe an empty chain.
// Evaluate every outgoing packet so a policy update also revokes existing flows.
func renderPolicy(p Policy, gateway string) string {
	var out strings.Builder
	out.WriteString("add table inet hostel_policy\ndelete table inet hostel_policy\ntable inet hostel_policy {\n")
	out.WriteString("set dns4 { type ipv4_addr; flags timeout; size 4096; }\nset dns6 { type ipv6_addr; flags timeout; size 4096; }\n")
	out.WriteString("chain output { type filter hook output priority 0; policy drop;\noifname lo accept\nct direction reply accept\n")
	fmt.Fprintf(&out, "ip daddr %s udp dport 53 accept\nip daddr %s tcp dport 53 accept\n", gateway, gateway)
	out.WriteString("udp dport 53 drop\ntcp dport 53 drop\n")
	writePolicyRules(&out, p)
	out.WriteString("}\n}\n")
	return out.String()
}
func writePolicyRules(out *strings.Builder, p Policy) {
	for _, action := range []string{"deny", "allow"} {
		verdict := "drop"
		if action == "allow" {
			verdict = "accept"
		}
		for _, rule := range p.Egress {
			prefix, ok := targetPrefix(rule.Target)
			if !ok || rule.Action != action {
				continue
			}
			family := "ip"
			if prefix.Addr().Is6() {
				family = "ip6"
			}
			fmt.Fprintf(out, "%s daddr %s %s\n", family, prefix.String(), verdict)
		}
	}
	out.WriteString("ip daddr @dns4 accept\nip6 daddr @dns6 accept\n")
	if p.DefaultAction == "allow" {
		out.WriteString("accept\n")
	}
}

func (e *linuxEndpoint) learnDNS(ctx context.Context, addresses []dnsAddress) error {
	_, err := run(ctx, renderDNSSet("hostel_policy", addresses), e.owner.ip, "netns", "exec", e.name, e.owner.nft, "-f", "-")
	return err
}

func renderDNSSet(table string, addresses []dnsAddress) string {
	var out strings.Builder
	// Flush and refill both sets in one transaction; retain unexpired answers
	// from other names and refresh repeated answers without duplicate-element errors.
	fmt.Fprintf(&out, "flush set inet %s dns4\nflush set inet %s dns6\n", table, table)
	seen := make(map[string]bool, len(addresses))
	for _, address := range addresses {
		ip := address.IP.String()
		if seen[ip] {
			continue
		}
		seen[ip] = true
		set := "dns4"
		if address.IP.Is6() {
			set = "dns6"
		}
		fmt.Fprintf(&out, "add element inet %s %s { %s timeout %ds }\n", table, set, ip, address.TTL)
	}
	return out.String()
}

// +spec=`Shared-netns egress is scoped by socket UID; DNS grants are allocation-local.`
// +link=`docs/network.md#共享网络下的-uid-策略`
func renderUIDPolicy(name string, uid, daemonUID uint32, dnsIP string, dnsPort int, p Policy) string {
	var out strings.Builder
	fmt.Fprintf(&out, "add table ip %s\ndelete table ip %s\ntable ip %s {\n", name, name, name)
	out.WriteString("chain dns { type nat hook output priority -110; policy accept;\n")
	for _, protocol := range []string{"udp", "tcp"} {
		fmt.Fprintf(&out, "meta skuid %d %s dport 53 dnat to %s:%d\n", uid, protocol, dnsIP, dnsPort)
	}
	out.WriteString("}\n}\n")
	fmt.Fprintf(&out, "add table inet %s\ndelete table inet %s\ntable inet %s {\n", name, name, name)
	out.WriteString("set dns4 { type ipv4_addr; flags timeout; size 4096; }\nset dns6 { type ipv6_addr; flags timeout; size 4096; }\n")
	out.WriteString("chain output { type filter hook output priority 0; policy accept;\n")
	// Guard this resolver even when another UID's own policy allows loopback.
	for _, protocol := range []string{"udp", "tcp"} {
		fmt.Fprintf(&out, "ip daddr %s %s dport %d meta skuid != %d meta skuid != %d drop\n", dnsIP, protocol, dnsPort, uid, daemonUID)
	}
	fmt.Fprintf(&out, "meta skuid %d jump workload\n}\nchain workload {\n", uid)
	// IPv6 is not part of the current external-connectivity contract. No blanket
	// loopback or established-flow allowance: updates must revoke existing flows.
	out.WriteString("meta nfproto ipv6 drop\nct direction reply accept\n")
	for _, protocol := range []string{"udp", "tcp"} {
		fmt.Fprintf(&out, "ip daddr %s %s dport %d accept\n", dnsIP, protocol, dnsPort)
	}
	out.WriteString("udp dport 53 drop\ntcp dport 53 drop\n")
	writePolicyRules(&out, p)
	out.WriteString("drop\n}\n}\n")
	return out.String()
}
