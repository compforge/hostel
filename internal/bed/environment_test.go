package bed

import (
	"context"
	"testing"
)

func TestEnvironmentDiagnosticsTrackCompositionProbe(t *testing.T) {
	m := newTestManager(t)
	if report := m.Diagnostics().Environment; report.ProbeStatus != EnvironmentProbeNotRun {
		t.Fatalf("initial environment report = %+v", report)
	}
	if err := m.ProbeEnvironment(context.Background()); err != nil {
		t.Fatal(err)
	}
	report := m.Diagnostics().Environment
	if report.ProbeStatus != EnvironmentProbePassed || report.StartedAt.IsZero() || report.FinishedAt.IsZero() || report.Error != "" {
		t.Fatalf("completed environment report = %+v", report)
	}
}
