package resource

import (
	"strings"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/tool"
)

func TestOffAccountingDoesNotProbe(t *testing.T) {
	tracker := NewConfigured(Config{Cgroup: tool.Off}).(*hostTracker)
	tracker.create = func() Tracker { t.Fatal("off accounting probed host"); return nil }
	if err := tracker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := tracker.Report().Tools["cgroup"]
	if s.Probe != "not_probed" || s.Selected || s.Reason != "disabled_by_config" {
		t.Fatalf("%+v", s)
	}
	if err := tracker.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestRequiredAccountingFailsStartup(t *testing.T) {
	tracker := NewConfigured(Config{Cgroup: tool.Required}).(*hostTracker)
	tracker.create = func() Tracker { return Noop("read-only cgroup") }
	if err := tracker.Start(t.Context()); err == nil || !strings.Contains(err.Error(), "read-only cgroup") {
		t.Fatalf("%v", err)
	}
	if err := tracker.Close(); err != nil {
		t.Fatal(err)
	}
}
