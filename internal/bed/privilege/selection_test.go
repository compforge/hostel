package privilege

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/qiankunli/hostel/internal/bed"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
	hostprivilege "github.com/qiankunli/hostel/internal/host/privilege"
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

func TestPreferredIdentityFallsBackWithoutSwitchingCapabilities(t *testing.T) {
	facts := hostfacts.Collect()
	facts.EffectiveCaps = 0
	s, err := Resolve(t.Context(), facts, Config{UID: os.Geteuid() + 10000, GID: os.Getegid() + 10000, Configured: true}, bed.Dorm, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s.User != CurrentBedUser() || s.Effective != Shared || !s.Probe.Succeeded() || !strings.Contains(s.Reason, "using daemon identity") {
		t.Fatalf("preferred identity fallback: %+v", s)
	}
	if s.Policy.UID != os.Geteuid() || s.Policy.GID != os.Getegid() {
		t.Fatalf("reported preference instead of actual identity: %+v", s.Policy)
	}
}

func TestPreferredIdentityProbeFailureFallsBack(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("foreign identity management is Linux-only")
	}
	facts := hostfacts.Collect()
	if os.Geteuid() == 0 && facts.EffectiveCaps&1 != 0 {
		if len(MissingBedIdentityCapabilities(facts.EffectiveCaps)) != 0 {
			t.Skip("requires identity-management capabilities to construct the child condition")
		}
		helper, err := hostprivilege.ProcessCredentialHelper()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), helper, "--bounding-set=-chown", "--no-new-privs", "--", os.Args[0], "-test.run=^TestPreferredIdentityProbeFailureFallsBack$", "-test.v")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("candidate probe failure regression: %v: %s", err, out)
		}
		return
	}
	// Model capabilities becoming unavailable after observation: selection
	// tries the preferred identity, but the real chown probe must reject it.
	for _, capability := range RequiredBedIdentityCapabilities() {
		facts.EffectiveCaps |= 1 << capability.Bit
	}
	root := t.TempDir()
	s, err := Resolve(t.Context(), facts, Config{UID: os.Geteuid() + 10000, GID: os.Getegid() + 10000, Configured: true}, bed.Dorm, root)
	if err != nil {
		t.Fatal(err)
	}
	if s.User != CurrentBedUser() || !strings.Contains(s.Reason, "using daemon identity") {
		t.Fatalf("failed preference was not reported: %+v", s)
	}
}

func TestPreferredIdentityKeepsUsableUser(t *testing.T) {
	if os.Geteuid() == 0 || os.Getegid() == 0 {
		t.Skip("this case uses an existing non-root identity")
	}
	facts := hostfacts.Collect()
	s, err := Resolve(t.Context(), facts, Config{UID: os.Geteuid(), GID: os.Getegid(), Configured: true}, bed.Dorm, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s.User != CurrentBedUser() || strings.Contains(s.Reason, "using daemon identity") {
		t.Fatalf("usable preference was replaced: %+v", s)
	}
}

func TestPreferredIdentityRejectsInvalidUser(t *testing.T) {
	for _, uid := range []int{0, -1} {
		_, err := Resolve(t.Context(), hostfacts.Collect(), Config{UID: uid, GID: 1000, Configured: true}, bed.Dorm, t.TempDir())
		if err == nil {
			t.Fatalf("accepted invalid preferred uid %d", uid)
		}
	}
}

func TestPreferredIdentityDoesNotBypassInheritedProbe(t *testing.T) {
	facts := hostfacts.Collect()
	facts.EffectiveCaps = 0
	_, err := Resolve(t.Context(), facts, Config{UID: 10000, GID: 10000, Configured: true}, bed.Dorm, filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("accepted an inherited identity without successful file access")
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
