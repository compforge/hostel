package manager

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/filesystem"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
)

func TestEnvironmentDiagnosticsTrackCompositionProbe(t *testing.T) {
	m := newTestManager(t)
	if report := m.Status().Environment; report.ProbeStatus != EnvironmentProbeNotRun {
		t.Fatalf("initial environment report = %+v", report)
	}
	if err := m.ProbeEnvironment(context.Background()); err != nil {
		t.Fatal(err)
	}
	report := m.Status().Environment
	if report.ProbeStatus != EnvironmentProbePassed || report.StartedAt.IsZero() || report.FinishedAt.IsZero() || report.Error != "" {
		t.Fatalf("completed environment report = %+v", report)
	}
}

type unavailableProbeFiles struct {
	isolation.Isolator
	err      error
	prepares int
}

func (f *unavailableProbeFiles) Prepare(context.Context, *bedfs.FS) error {
	f.prepares++
	return f.err
}

// +case:id=environment_failure_preserves_subsystems,desc=`A selected filesystem fails during real Bed preparation`,expect=`Report the failure and clean the probe Bed without retrying weaker subsystem choices`
func TestEnvironmentFailureDoesNotReselectSubsystems(t *testing.T) {
	m := newTestManager(t)
	t.Cleanup(func() {
		if err := m.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	original := errors.New("selected filesystem preparation failed")
	files := &unavailableProbeFiles{Isolator: m.iso, err: original}
	m.iso = files
	m.files = filesystem.NewManager(files, m.owners.Filesystem)
	before := m.network.Status().Effective
	err := m.ProbeEnvironment(t.Context())
	if !errors.Is(err, original) || files.prepares != 1 || m.iso != files || m.network.Status().Effective != before {
		t.Fatalf("probe reselected or lost failure: prepares=%d err=%v", files.prepares, err)
	}
	report := m.Status().Environment
	if report.ProbeStatus != EnvironmentProbeFailed || !strings.Contains(report.Error, original.Error()) {
		t.Fatalf("missing failure report: %+v", report)
	}
	if len(m.beds) != 0 || len(m.initializations) != 0 {
		t.Fatal("failed environment probe retained Bed allocations")
	}
}
