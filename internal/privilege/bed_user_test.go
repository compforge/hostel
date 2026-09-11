package privilege

import "testing"

func TestBedUserRejectsRoot(t *testing.T) {
	for _, test := range []struct{ uid, gid int }{{0, 1000}, {1000, 0}, {-1, 1000}} {
		if _, err := NewBedUser(test.uid, test.gid); err == nil {
			t.Fatalf("NewBedUser(%d, %d) accepted a root/invalid identity", test.uid, test.gid)
		}
	}
}

func TestPrivilegeReportUsesBedIdentityRequirements(t *testing.T) {
	report := NewReport(BedUserReport{Strategy: "per_bed", UIDMin: 200000, UIDMax: 299999}, 0)
	want := RequiredBedIdentityCapabilities()
	if len(report.Requirements.Capabilities) != len(want) || len(report.Requirements.MissingCapabilities) != len(want) {
		t.Fatalf("requirements = %+v, want %d required and missing", report.Requirements, len(want))
	}
	if report.Requirements.Satisfied || report.PreconditionsSatisfied {
		t.Fatalf("zero capabilities reported ready: %+v", report)
	}
	var all uint64
	for _, capability := range want {
		all |= uint64(1) << capability.Bit
	}
	report = NewReport(BedUserReport{Strategy: "per_bed", UIDMin: 200000, UIDMax: 299999}, all)
	if !report.Requirements.Satisfied {
		t.Fatalf("complete capabilities reported missing: %+v", report.Requirements)
	}
}
