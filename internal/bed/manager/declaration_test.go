package manager

import (
	"context"
	"errors"
	"testing"

	hostel "github.com/qiankunli/hostel/internal"
	model "github.com/qiankunli/hostel/internal/bed"
)

func testWorkspacePillows() []model.PillowSpec {
	return []model.PillowSpec{{Name: "main", Mounts: []model.VolumeMount{{Volume: "workspace", Path: "/workspace"}}}}
}

// declareTestBed supplies the fixture's workload before waiting for readiness.
// Existing declarations are used unchanged by tests that configured their own Pillows.
func declareTestBed(m *Manager, ctx context.Context, name string) (*Resident, error) {
	if b, err := m.Ensure(ctx, name); !errors.Is(err, hostel.ErrNotFound) {
		return b, err
	}
	if _, err := m.InitializeBed(ctx, name, CreateOptions{Pillows: testWorkspacePillows()}); err != nil {
		return nil, err
	}
	return m.Ensure(ctx, name)
}

func TestBedRequiresExplicitPillows(t *testing.T) {
	m := storeTestManager(t, t.TempDir(), newFakeStore())
	defer m.Close(t.Context())
	for _, pillows := range [][]model.PillowSpec{nil, {}} {
		if _, err := m.InitializeBed(t.Context(), "missing", CreateOptions{Pillows: pillows}); err == nil {
			t.Fatal("accepted empty declaration")
		}
	}
	if m.started || len(m.localIdentities) != 0 || len(m.initializations) != 0 {
		t.Fatal("invalid declaration mutated manager")
	}
	if _, err := m.Ensure(t.Context(), "missing"); !errors.Is(err, hostel.ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
	if _, err := m.InitializeBed(t.Context(), "bare", CreateOptions{Pillows: []model.PillowSpec{{Name: "agent"}}}); err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(t.Context(), "bare")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Spec().Pillows) != 1 || len(b.Spec().Pillows[0].Mounts) != 0 {
		t.Fatal("implicit Pillow or mount")
	}
}
