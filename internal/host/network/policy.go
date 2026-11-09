package network

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"

	hostel "github.com/qiankunli/hostel/internal"
)

var ErrUnavailable = errors.New("network policy is unavailable")
var ErrInvalidPolicy = hostel.WrapError(hostel.ErrInvalidArgument, "network policy", hostel.CauseRequest, nil)

// PolicyUnchangedError is evidence that a failed transaction did not change
// the installed rules. Timeouts and lost replies must not use this wrapper.
type PolicyUnchangedError struct{ Err error }

func (e *PolicyUnchangedError) Error() string { return e.Err.Error() }
func (e *PolicyUnchangedError) Unwrap() error { return e.Err }

type Rule struct {
	Action string `json:"action"`
	Target string `json:"target"`
}
type Policy struct {
	DefaultAction string `json:"defaultAction"`
	Egress        []Rule `json:"egress"`
}
type PolicyMutation struct {
	Replace *Policy
	Merge   []Rule
	Remove  []string
}

// NormalizePolicy validates before any nft input is generated. Targets are
// canonical IPs/CIDRs or DNS labels, never arbitrary nft expressions.
func NormalizePolicy(p Policy) (Policy, error) {
	if p.DefaultAction == "" {
		p.DefaultAction = "deny"
		if len(p.Egress) == 0 {
			p.DefaultAction = "allow"
		}
	}
	if p.DefaultAction != "allow" && p.DefaultAction != "deny" {
		return Policy{}, fmt.Errorf("%w: defaultAction must be allow or deny", ErrInvalidPolicy)
	}
	if len(p.Egress) > 1024 {
		return Policy{}, fmt.Errorf("%w: at most 1024 rules", ErrInvalidPolicy)
	}
	out := Policy{DefaultAction: p.DefaultAction, Egress: make([]Rule, 0, len(p.Egress))}
	for _, rule := range p.Egress {
		if rule.Action != "allow" && rule.Action != "deny" {
			return Policy{}, fmt.Errorf("%w: action must be allow or deny", ErrInvalidPolicy)
		}
		target, err := normalizeTarget(rule.Target)
		if err != nil {
			return Policy{}, err
		}
		rule.Target = target
		out.Egress = append(out.Egress, rule)
	}
	return out, nil
}
func normalizeTarget(target string) (string, error) {
	target = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(target), "."))
	if ip, err := netip.ParseAddr(target); err == nil && ip.Zone() == "" {
		return ip.Unmap().String(), nil
	}
	if prefix, err := netip.ParsePrefix(target); err == nil {
		return prefix.Masked().String(), nil
	}
	name := strings.TrimPrefix(target, "*.")
	if name == "" || len(name) > 253 {
		return "", fmt.Errorf("%w: invalid target", ErrInvalidPolicy)
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("%w: invalid DNS target", ErrInvalidPolicy)
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-') {
				return "", fmt.Errorf("%w: invalid DNS target", ErrInvalidPolicy)
			}
		}
	}
	return target, nil
}
func targetPrefix(target string) (netip.Prefix, bool) {
	if ip, err := netip.ParseAddr(target); err == nil {
		return netip.PrefixFrom(ip, ip.BitLen()), true
	}
	p, err := netip.ParsePrefix(target)
	return p, err == nil
}
func (p Policy) domainAction(name string) string {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	action := p.DefaultAction
	for _, rule := range p.Egress {
		if _, ip := targetPrefix(rule.Target); ip {
			continue
		}
		match := rule.Target == name
		if strings.HasPrefix(rule.Target, "*.") {
			match = strings.HasSuffix(name, rule.Target[1:])
		}
		if match {
			if rule.Action == "deny" {
				return "deny"
			}
			action = "allow"
		}
	}
	return action
}
func (p Policy) deniesIP(ip netip.Addr) bool {
	for _, rule := range p.Egress {
		if prefix, ok := targetPrefix(rule.Target); ok && rule.Action == "deny" && prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// The lock serializes kernel commits and DNS learning. In-flight DNS replies
// from an older policy revision cannot re-open addresses after a policy update.
type policyControl struct {
	mu        sync.Mutex
	current   Policy
	revision  uint64
	closed    bool
	uncertain bool
	learned   map[netip.Addr]time.Time
	apply     func(context.Context, Policy) error
	learn     func(context.Context, []dnsAddress) error
}

// ApplyMutation validates the entire desired policy before any state is published.
func ApplyMutation(current Policy, mutation PolicyMutation) (Policy, error) {
	next := current
	if mutation.Replace != nil {
		next = *mutation.Replace
	}
	if mutation.Merge != nil {
		next.Egress = append([]Rule(nil), next.Egress...)
		for _, rule := range mutation.Merge {
			normalized, err := normalizeTarget(rule.Target)
			if err != nil {
				return Policy{}, err
			}
			rule.Target = normalized
			kept := next.Egress[:0]
			for _, old := range next.Egress {
				if old.Target != rule.Target {
					kept = append(kept, old)
				}
			}
			next.Egress = append(kept, rule)
		}
	}
	if mutation.Remove != nil {
		remove := make(map[string]bool, len(mutation.Remove))
		for _, target := range mutation.Remove {
			normalized, err := normalizeTarget(target)
			if err != nil {
				return Policy{}, err
			}
			remove[normalized] = true
		}
		next.Egress = make([]Rule, 0, len(current.Egress))
		for _, rule := range current.Egress {
			if !remove[rule.Target] {
				next.Egress = append(next.Egress, rule)
			}
		}
	}
	return NormalizePolicy(next)
}

func (p *policyControl) update(ctx context.Context, mutation PolicyMutation) (Policy, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return Policy{}, ErrUnavailable
	}
	changed := mutation.Replace != nil || mutation.Merge != nil || mutation.Remove != nil
	if changed {
		normalized, err := ApplyMutation(p.current, mutation)
		if err != nil {
			return Policy{}, err
		}
		if err := p.apply(ctx, normalized); err != nil {
			var unchanged *PolicyUnchangedError
			if !errors.As(err, &unchanged) {
				p.uncertain = true
			}
			return Policy{}, fmt.Errorf("apply network policy: %w", err)
		}
		p.uncertain = false
		p.current = normalized
		p.revision++
		p.learned = nil
	}
	if p.uncertain {
		return Policy{}, ErrUnavailable
	}
	policy := p.current
	policy.Egress = append([]Rule{}, policy.Egress...)
	return policy, nil
}

// NetworkPolicy only operates an already acquired namespace; it never
// allocates networking or silently accepts a policy on a shared network.
func (m *Pool) NetworkPolicy(ctx context.Context, key string, mutation PolicyMutation) (Policy, error) {
	if m == nil || !m.Status().Available {
		return Policy{}, ErrUnavailable
	}
	m.mu.Lock()
	lease := m.allocations[key]
	closed := m.closed
	m.mu.Unlock()
	if lease == nil || closed {
		return Policy{}, ErrUnavailable
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if !lease.active || !lease.current() {
		return Policy{}, ErrUnavailable
	}
	provider, ok := lease.endpoint.(interface{ Policy() *policyControl })
	if !ok {
		return Policy{}, ErrUnavailable
	}
	return provider.Policy().update(ctx, mutation)
}
