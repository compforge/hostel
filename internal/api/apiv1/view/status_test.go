package view

import (
	"encoding/json"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/filesystem"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/bed/resource"
	"github.com/qiankunli/hostel/internal/bed/tool"
)

func TestComponentToolStatusJSON(t *testing.T) {
	selected := map[string]tool.Status{
		"example": tool.Describe(tool.Required, tool.Requirements{Tools: []string{"helper"}}, true, true, true, ""),
	}
	reports := map[string]any{
		"filesystem":             filesystem.Status{Tools: selected},
		"filesystem diagnostics": isolation.DiagnosticsReport{Tools: selected},
		"network":                network.Status{Tools: selected},
		"resource accounting":    resource.Report{Tools: selected},
	}
	for name, report := range reports {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			if _, exists := fields["features"]; exists {
				t.Fatal("obsolete features field returned")
			}
			var tools map[string]tool.Status
			if err := json.Unmarshal(fields["tools"], &tools); err != nil {
				t.Fatal(err)
			}
			got := tools["example"]
			if got.Policy != tool.Required || got.Probe != "available" || !got.Selected ||
				len(got.Requirements.Tools) != 1 || got.Requirements.Tools[0] != "helper" {
				t.Fatalf("tool status lost fields: %+v", got)
			}
		})
	}
}
