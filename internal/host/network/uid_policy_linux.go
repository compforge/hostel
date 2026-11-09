//go:build linux

package network

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/qiankunli/go-stdx/randx"
	hostel "github.com/qiankunli/hostel/internal"
)

// uidPolicyBackend installs only socket-owner-scoped rules in the current netns.
// Identity allocation and stopping its processes belong to the caller.
type uidPolicyBackend struct {
	store     *AllocationStore
	mu        sync.Mutex
	nft       string
	ports     *PortManager
	upstreams []string
	leases    map[uint32]*uidPolicyLease
	closed    bool
}

type uidPolicyLease struct {
	closeMu  sync.Mutex
	owner    *uidPolicyBackend
	uid      uint32
	name     string
	dns      *dnsForwarder
	policy   *policyControl
	table    bool
	record   *allocationRecord
	released bool // protected by policy.mu, including incomplete acquisitions
}

// ProbeUIDPolicy validates the complete ruleset without changing routing or
// sysctls. Actual installation also verifies netlink mutation permissions.
func ProbeUIDPolicy(ctx context.Context, ports *PortManager, stores ...*AllocationStore) (UIDPolicyProvider, Probe) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	probe := Probe{Stage: "tools", Cause: hostel.CauseCarrier}
	b := &uidPolicyBackend{ports: ports, leases: make(map[uint32]*uidPolicyLease)}
	if len(stores) > 0 {
		b.store = stores[0]
	}
	var err error
	b.nft, err = exec.LookPath("nft")
	if err == nil {
		probe.Stage = "dns"
		b.upstreams, _, err = readResolvers()
	}
	if err == nil {
		probe.Stage, probe.Attempted = "uid_rules", true
		name := "hu_probe_" + randx.Hex(6)
		rules := renderUIDPolicy(name, 4294967294, uint32(os.Geteuid()), "127.0.0.1", 53535, Policy{DefaultAction: "deny"})
		_, err = run(ctx, rules, b.nft, "--check", "-f", "-")
		if err == nil {
			// An empty, uniquely owned table exercises create/delete with no packet effect.
			_, err = run(ctx, "create table inet "+name+"\ndelete table inet "+name+"\n", b.nft, "-f", "-")
		}
	}
	probe.DurationMS = time.Since(started).Milliseconds()
	if err != nil {
		probe.Err = hostel.WrapError(hostel.ErrUnavailable, "UID network policy probe", probe.Cause, err)
		probe.Error = probe.Err.Error()
		return nil, probe
	}
	probe.Stage, probe.Cause = "ready", ""
	return b, probe
}

func (b *uidPolicyBackend) Acquire(ctx context.Context, key string, uid uint32) (PolicyLease, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, ErrUnavailable
	}
	if uid == 0 || uid == uint32(os.Geteuid()) {
		return nil, fmt.Errorf("UID policy requires a dedicated non-daemon identity")
	}
	if b.leases[uid] != nil {
		return nil, fmt.Errorf("UID %d still owns a network policy allocation", uid)
	}
	e := &uidPolicyLease{owner: b, uid: uid, name: "hu_" + randx.Hex(8)}
	var err error
	e.record, err = b.store.Add(allocationRecord{Kind: "uid", Key: key, Name: e.name, UID: uid})
	if err != nil {
		return nil, err
	}
	e.policy = &policyControl{current: Policy{DefaultAction: "allow"}, apply: e.apply, learn: e.learn}
	e.dns = newDNSForwarder("127.0.0.1", b.upstreams)
	e.dns.address, e.dns.ports, e.dns.owner = "127.0.0.1:0", b.ports, e.name+"/dns"
	e.dns.policy = e.policy
	b.leases[uid] = e // Own partial allocation before any mutation.
	if err := e.dns.Start(); err != nil {
		return e, err
	}
	// Include the table in cleanup even if nft's reply is lost after commit.
	e.table = true
	if err := e.apply(ctx, e.policy.current); err != nil {
		return e, err
	}
	return e, nil
}
func (e *uidPolicyLease) apply(ctx context.Context, p Policy) error {
	host, port, err := net.SplitHostPort(e.dns.address)
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return err
	}
	_, err = run(ctx, renderUIDPolicy(e.name, e.uid, uint32(os.Geteuid()), host, n, p), e.owner.nft, "-f", "-")
	return err
}
func (e *uidPolicyLease) learn(ctx context.Context, addresses []dnsAddress) error {
	_, err := run(ctx, renderDNSSet(e.name, addresses), e.owner.nft, "-f", "-")
	return err
}
func (e *uidPolicyLease) Update(ctx context.Context, change PolicyMutation) (PolicyStatus, error) {
	return e.policy.update(ctx, change)
}
func (e *uidPolicyLease) Close(ctx context.Context) error {
	e.closeMu.Lock()
	defer e.closeMu.Unlock()
	e.policy.mu.Lock()
	defer e.policy.mu.Unlock()
	if e.released {
		return nil
	}
	e.policy.closed = true // Fence DNS learning and updates before removing any rules.
	if e.table {
		// add/delete is idempotent when creation or an earlier deletion timed out.
		script := fmt.Sprintf("add table inet %s\ndelete table inet %s\nadd table ip %s\ndelete table ip %s\n", e.name, e.name, e.name, e.name)
		if _, err := run(ctx, script, e.owner.nft, "-f", "-"); err != nil {
			return err
		}
		e.table = false
	}
	// DNS handlers may need policy.mu; join them after releasing it.
	e.policy.mu.Unlock()
	e.dns.Close()
	e.policy.mu.Lock()
	if err := e.owner.store.Remove(e.record); err != nil {
		return err
	}
	e.released = true
	e.owner.mu.Lock()
	if e.owner.leases[e.uid] == e {
		delete(e.owner.leases, e.uid)
	}
	e.owner.mu.Unlock()
	return nil
}
func (b *uidPolicyBackend) Close(ctx context.Context) error {
	b.mu.Lock()
	b.closed = true
	leases := make([]*uidPolicyLease, 0, len(b.leases))
	for _, e := range b.leases {
		leases = append(leases, e)
	}
	b.mu.Unlock()
	var result error
	for _, e := range leases {
		result = errors.Join(result, e.Close(ctx))
	}
	return result
}
