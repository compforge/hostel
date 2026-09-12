//go:build linux

package network

import (
	"context"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPolicyRulesDenyBeforeAllow(t *testing.T) {
	p := Policy{DefaultAction: "allow", Egress: []Rule{{Action: "allow", Target: "10.0.0.0/8"}, {Action: "deny", Target: "10.2.0.0/16"}}}
	rules := renderPolicy(p, "198.18.0.1")
	if strings.Index(rules, "10.2.0.0/16 drop") > strings.Index(rules, "10.0.0.0/8 accept") || strings.Contains(rules, "ct state established") {
		t.Fatal(rules)
	}
}
func TestLinuxPolicySeparatesBedsAndRevokesAccess(t *testing.T) {
	if os.Getenv("HOSTEL_NETWORK_TEST") != "enabled" {
		t.Skip("requires disposable privileged Linux container")
	}
	m := New(t.Context())
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	if !m.Status().Enabled {
		t.Fatalf("network unavailable: %+v", m.Status())
	}
	for _, id := range []string{"policy-a", "policy-b"} {
		if _, err := m.Acquire(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	a, b := m.beds["policy-a"].endpoint.(*linuxEndpoint), m.beds["policy-b"].endpoint.(*linuxEndpoint)
	if _, err := m.NetworkPolicy(t.Context(), "policy-a", PolicyMutation{Replace: &Policy{DefaultAction: "deny"}}); err != nil {
		t.Fatal(err)
	}
	blocked, cancel := context.WithTimeout(t.Context(), time.Second)
	err := probeConnectivity(blocked, a)
	cancel()
	if err == nil {
		t.Fatal("deny-all allowed Bed A to connect")
	}
	if err := probeConnectivity(t.Context(), b); err != nil {
		t.Fatalf("Bed A policy affected Bed B: %v", err)
	}
	if _, err := m.NetworkPolicy(t.Context(), "policy-a", PolicyMutation{Merge: []Rule{{Action: "allow", Target: a.gateway.String()}}}); err != nil {
		t.Fatal(err)
	}
	if err := probeConnectivity(t.Context(), a); err != nil {
		t.Fatalf("allow rule did not apply: %v", err)
	}
	if _, err := m.NetworkPolicy(t.Context(), "policy-a", PolicyMutation{Remove: []string{a.gateway.String()}}); err != nil {
		t.Fatal(err)
	}
	blocked, cancel = context.WithTimeout(t.Context(), time.Second)
	err = probeConnectivity(blocked, a)
	cancel()
	if err == nil {
		t.Fatal("deleted allow rule still permits traffic")
	}
	// Exercise the real nft set syntax twice, including duplicate DNS answers.
	for range 2 {
		if err := a.learnDNS(t.Context(), []dnsAddress{{IP: netip.MustParseAddr("203.0.113.1"), TTL: 30}}); err != nil {
			t.Fatal(err)
		}
	}
}
