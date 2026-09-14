//go:build !linux

package isolation

import (
	"testing"

	"github.com/qiankunli/hostel/internal/feature"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

func TestUnsupportedBwrapDoesNotAdvertisePrivateFiles(t *testing.T) {
	iso, err := Resolve(hostfacts.Snapshot{}, Config{
		Level: "private", Landlock: feature.Off, UID: feature.Off,
		PRoot: feature.Off, Pathshim: feature.Off,
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	report := iso.(Report)
	if iso.Level() != Shared || report.Ceiling() != Shared {
		t.Fatalf("unsupported boundary advertised isolation: level=%s ceiling=%s", iso.Level(), report.Ceiling())
	}
	if status := report.Diagnostics().Features["bwrap"]; status.Probe != "unavailable" || status.Selected {
		t.Fatalf("unsupported bwrap reported available: %+v", status)
	}
}
