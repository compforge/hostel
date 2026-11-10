package manager

import (
	"context"
	"errors"
	"os/exec"
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
	before := m.network.Status().Isolation.Level
	err := m.ProbeEnvironment(t.Context())
	if !errors.Is(err, original) || files.prepares != 1 || m.iso != files || m.network.Status().Isolation.Level != before {
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

// The composed probe must retain a bounded diagnostic from the failed process,
// not reduce a useful helper error to exit code 1.
type failingCommandProbe struct{ isolation.Isolator }

func (f failingCommandProbe) Wrap(cmd *exec.Cmd, _ *bedfs.FS, _ string) error {
	cmd.Path = "/bin/sh"
	cmd.Args = []string{"/bin/sh", "-c", `printf 'probe-diagnostic: %05000d' 0 >&2; exit 7`}
	return nil
}
func TestEnvironmentFailureRetainsBoundedStderr(t *testing.T) {
	m := newTestManager(t)
	defer m.Close(context.Background())
	files := failingCommandProbe{m.iso}
	m.iso = files
	m.files = filesystem.NewManager(files, m.owners.Filesystem)
	err := m.ProbeEnvironment(t.Context())
	if err == nil || !strings.Contains(err.Error(), "probe-diagnostic:") || !strings.Contains(err.Error(), "ExitCode:7") || len(err.Error()) > 4500 {
		t.Fatalf("probe diagnostic: %v", err)
	}
}
