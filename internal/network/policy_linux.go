//go:build linux

package network

import (
	"context"
	"fmt"
	"strings"
)

func (e *linuxEndpoint) Policy() *policyControl { return e.policy }
func (e *linuxEndpoint) applyPolicy(ctx context.Context, p Policy) error {
	_, err := run(ctx, renderPolicy(p, e.gateway.String()), e.owner.ip, "netns", "exec", e.name, e.owner.nft, "-f", "-")
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
			fmt.Fprintf(&out, "%s daddr %s %s\n", family, prefix.String(), verdict)
		}
	}
	out.WriteString("ip daddr @dns4 accept\nip6 daddr @dns6 accept\n")
	if p.DefaultAction == "allow" {
		out.WriteString("accept\n")
	}
	out.WriteString("}\n}\n")
	return out.String()
}
func (e *linuxEndpoint) learnDNS(ctx context.Context, addresses []dnsAddress) error {
	var out strings.Builder
	// Flush and refill both sets in one transaction; retain unexpired answers
	// from other names and refresh repeated answers without duplicate-element errors.
	out.WriteString("flush set inet hostel_policy dns4\nflush set inet hostel_policy dns6\n")
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
		fmt.Fprintf(&out, "add element inet hostel_policy %s { %s timeout %ds }\n", set, ip, address.TTL)
	}
	_, err := run(ctx, out.String(), e.owner.ip, "netns", "exec", e.name, e.owner.nft, "-f", "-")
	return err
}
