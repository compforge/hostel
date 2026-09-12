package network

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func newPolicyForTest() *policyControl {
	return &policyControl{current: Policy{DefaultAction: "allow"}, apply: func(context.Context, Policy) error { return nil }, learn: func(context.Context, []dnsAddress) error { return nil }}
}
func TestPolicyMutationsAndFailedCommit(t *testing.T) {
	p := newPolicyForTest()
	ctx := context.Background()
	status, err := p.update(ctx, PolicyMutation{Replace: &Policy{DefaultAction: "deny", Egress: []Rule{{Action: "allow", Target: "EXAMPLE.COM."}}}})
	if err != nil || status.Policy.Egress[0].Target != "example.com" {
		t.Fatalf("replace = %+v %v", status, err)
	}
	status, err = p.update(ctx, PolicyMutation{Merge: []Rule{{Action: "deny", Target: "example.com"}, {Action: "allow", Target: "10.1.2.3/8"}}})
	if err != nil || len(status.Policy.Egress) != 2 || status.Policy.Egress[0].Action != "deny" || status.Policy.Egress[1].Target != "10.0.0.0/8" {
		t.Fatalf("merge = %+v %v", status, err)
	}
	status, err = p.update(ctx, PolicyMutation{Remove: []string{"EXAMPLE.COM."}})
	if err != nil || len(status.Policy.Egress) != 1 || status.Policy.DefaultAction != "deny" {
		t.Fatalf("delete = %+v %v", status, err)
	}
	p.apply = func(context.Context, Policy) error { return errors.New("kernel denied update") }
	_, err = p.update(ctx, PolicyMutation{Replace: &Policy{DefaultAction: "allow"}})
	if err == nil {
		t.Fatal("kernel error swallowed")
	}
	status, _ = p.update(ctx, PolicyMutation{})
	if status.Policy.DefaultAction != "deny" || len(status.Policy.Egress) != 1 {
		t.Fatal("failed kernel update changed reported policy")
	}
}
func TestPolicyRejectsInvalidTargetsAndActions(t *testing.T) {
	for _, target := range []string{"1.2.3.4; accept", "foo\nbar", "*", "a..b", "https://example.com", "10.0.0.0/999", "::1%lo"} {
		_, err := NormalizePolicy(Policy{Egress: []Rule{{Action: "allow", Target: target}}})
		if !errors.Is(err, ErrInvalidPolicy) {
			t.Errorf("target %q accepted", target)
		}
	}
	if _, err := NormalizePolicy(Policy{DefaultAction: "oops"}); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatal("invalid action accepted")
	}
	p, err := NormalizePolicy(Policy{Egress: []Rule{{Action: "allow", Target: "*.example.com"}, {Action: "deny", Target: "private.example.com"}, {Action: "deny", Target: "169.254.169.254"}}})
	if err != nil || p.domainAction("www.example.com.") != "allow" || p.domainAction("private.example.com") != "deny" || p.domainAction("example.com") != "deny" || !p.deniesIP(netip.MustParseAddr("169.254.169.254")) {
		t.Fatalf("policy matching: %+v %v", p, err)
	}
}
func dnsTestMessages(t *testing.T) ([]byte, []byte) {
	t.Helper()
	name, _ := dnsmessage.NewName("api.example.com.")
	query := dnsmessage.Message{Header: dnsmessage.Header{ID: 42}, Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}
	raw, err := query.Pack()
	if err != nil {
		t.Fatal(err)
	}
	query.Response = true
	query.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 20}, Body: &dnsmessage.AResource{A: [4]byte{203, 0, 113, 1}}}}
	response, err := query.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return raw, response
}
func TestDNSPolicyPublishesOnlyAfterKernelCommit(t *testing.T) {
	p := newPolicyForTest()
	_, err := p.update(t.Context(), PolicyMutation{Replace: &Policy{DefaultAction: "deny", Egress: []Rule{{Action: "allow", Target: "*.example.com"}}}})
	if err != nil {
		t.Fatal(err)
	}
	query, response := dnsTestMessages(t)
	learned := 0
	p.learn = func(_ context.Context, ips []dnsAddress) error {
		learned++
		if len(ips) != 1 || ips[0].IP.String() != "203.0.113.1" {
			t.Errorf("learned: %+v", ips)
		}
		return nil
	}
	if _, err := p.exchange(t.Context(), query, func() ([]byte, error) { return response, nil }); err != nil || learned != 1 {
		t.Fatalf("DNS learn: %v %d", err, learned)
	}
	p.learn = func(context.Context, []dnsAddress) error { return errors.New("nft failed") }
	if out, err := p.exchange(t.Context(), query, func() ([]byte, error) { return response, nil }); err == nil || len(out) > 0 {
		t.Fatal("DNS response escaped failed kernel update")
	}
}
func TestPolicyChangeDiscardsInFlightDNS(t *testing.T) {
	p := newPolicyForTest()
	query, response := dnsTestMessages(t)
	out, err := p.exchange(t.Context(), query, func() ([]byte, error) {
		_, err := p.update(t.Context(), PolicyMutation{Replace: &Policy{DefaultAction: "deny"}})
		return response, err
	})
	if err != nil {
		t.Fatal(err)
	}
	var got dnsmessage.Message
	if err := got.Unpack(out); err != nil || got.RCode != dnsmessage.RCodeRefused || len(got.Answers) > 0 {
		t.Fatalf("stale reply = %+v %v", got, err)
	}
}
func TestDisabledNetworkRejectsPolicies(t *testing.T) {
	var m *Pool
	if _, err := m.NetworkPolicy(t.Context(), "allocation", PolicyMutation{Replace: &Policy{DefaultAction: "deny"}}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

type policyEndpointForTest struct {
	fakeEndpoint
	control *policyControl
}

func (e *policyEndpointForTest) Policy() *policyControl { return e.control }

func TestNetworkPolicyRejectsRetiredAttachment(t *testing.T) {
	ctx := t.Context()
	ep := &policyEndpointForTest{control: newPolicyForTest()}
	m := testPool(nil)
	lease := &attachment{manager: m, key: "allocation", endpoint: ep, active: true}
	m.allocations[lease.key] = lease
	if _, err := m.NetworkPolicy(ctx, "allocation", PolicyMutation{}); err != nil {
		t.Fatal(err)
	}
	ep.err = errors.New("cleanup pending")
	if err := lease.Close(ctx); err == nil {
		t.Fatal("expected cleanup failure")
	}
	// Retained cleanup ownership must not make a retired namespace eligible
	// for policy reads or updates.
	for _, mutation := range []PolicyMutation{{}, {Replace: &Policy{DefaultAction: "deny"}}} {
		if _, err := m.NetworkPolicy(ctx, "allocation", mutation); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("retired policy operation: %v", err)
		}
	}
	if ep.control.current.DefaultAction != "allow" {
		t.Fatal("retired attachment policy changed")
	}
}
