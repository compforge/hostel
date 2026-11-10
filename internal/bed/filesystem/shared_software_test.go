package filesystem

import (
	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/tool"
	"path/filepath"
	"testing"
)

func TestSharedSoftwareFailurePoliciesAndStatusSnapshot(t *testing.T) {
	for _, policy := range []tool.Policy{tool.Auto, tool.Required, tool.Off} {
		t.Run(string(policy), func(t *testing.T) {
			m := NewManager(carrierOnly{}, bed.NewOwners().Filesystem)
			path := filepath.Join(t.TempDir(), "missing")
			m.softwareGroupAvailable = true
			m.sharedSoftware = []sharedDirectory{{Path: path, GID: 1000, Policy: policy}}
			err := m.Start(t.Context())
			if (err != nil) != (policy == tool.Required) {
				t.Fatalf("policy %s: %v", policy, err)
			}
			s := m.Status().SharedSoftware
			if len(s) != 1 || s[0].Tool.Selected {
				t.Fatalf("incorrect unavailable status: %+v", s)
			}
			if policy == tool.Off && s[0].Tool.Probe != "not_probed" {
				t.Fatal("off probed filesystem")
			}
			if policy != tool.Off && s[0].Tool.Probe != "unavailable" {
				t.Fatal("failure was not reported")
			}
			s[0].Tool.Requirements.Conditions[0] = "mutated"
			if m.Status().SharedSoftware[0].Tool.Requirements.Conditions[0] == "mutated" {
				t.Fatal("status aliases mutable storage")
			}
			if err := m.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
