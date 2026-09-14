package privilege

import (
	"os"
	"reflect"
	"testing"

	"github.com/qiankunli/hostel/internal/bed"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

func TestSharedIdentityBaselineNeedsNoSwitchingCapabilities(t *testing.T) {
	facts := hostfacts.Collect()
	facts.EffectiveCaps = 0 // Selection must not require caps for inherited identity.
	s, err := Resolve(t.Context(), facts, Config{}, bed.Suite, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s.Effective != Shared || !s.Probe.Succeeded() || s.User != CurrentBedUser() || !reflect.DeepEqual(s.Supported, []Level{Shared}) {
		t.Fatalf("baseline: %+v", s)
	}
}

func TestIdentityFallbackRetainsCapabilityFacts(t *testing.T) {
	s := Selection{Expected: Dedicated, Effective: Dedicated, User: CurrentBedUser(), Supported: []Level{Shared, Dedicated}, Policy: BedUserReport{Strategy: "per_bed", UIDMin: UIDMin, UIDMax: UIDMax}}
	next, ok := s.WithoutDedicatedIdentity("composition rejected")
	if !ok || next.Effective != Shared || next.Expected != Dedicated || next.Policy.Strategy != "fixed" || next.Policy.UID != os.Geteuid() || !reflect.DeepEqual(next.Supported, s.Supported) {
		t.Fatalf("fallback: %+v", next)
	}
	if _, ok := next.WithoutDedicatedIdentity("again"); ok {
		t.Fatal("repeated fallback")
	}
}

func TestExplicitIdentityDoesNotSilentlyFallBack(t *testing.T) {
	facts := hostfacts.Collect()
	facts.EffectiveCaps = 0
	_, err := Resolve(t.Context(), facts, Config{UID: os.Geteuid() + 10000, GID: os.Getegid() + 10000, Explicit: true}, bed.Dorm, t.TempDir())
	if err == nil {
		t.Fatal("accepted explicit foreign identity without its prerequisites")
	}
}

func TestPrivilegeOwnsDedicatedAllocationWithoutFilesystem(t *testing.T) {
	policy := BedUserReport{Strategy: "per_bed", UIDMin: UIDMin, UIDMax: UIDMax}
	m, err := NewManager(policy, CurrentBedUser(), 0, bed.NewOwners().Privilege, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, b := bed.New("a", "", bed.Spec{}), bed.New("b", "", bed.Spec{})
	for _, model := range []*bed.Bed{a, b} {
		if err := m.Prepare(t.Context(), model); err != nil {
			t.Fatal(err)
		}
		u := m.User(model)
		if u.UID() < UIDMin || u.UID() > UIDMax || u.GID() != u.UID() {
			t.Fatalf("dedicated allocation=%+v", u)
		}
	}
	if m.User(a) == m.User(b) {
		t.Fatal("Beds share a dedicated identity")
	}
	if report := m.Status(); report.BedUser != policy || report.ReservedUsers != 2 {
		t.Fatalf("report=%+v", report)
	}
	for _, model := range []*bed.Bed{a, b} {
		if err := m.Forget(t.Context(), model); err != nil {
			t.Fatal(err)
		}
	}
	if m.Status().ReservedUsers != 0 {
		t.Fatal("forgotten identities retained leases")
	}
}
