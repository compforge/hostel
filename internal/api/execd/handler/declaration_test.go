package handler

import (
	"context"
	"errors"

	hostel "github.com/qiankunli/hostel/internal"
	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/manager"
)

func testWorkspacePillows() []model.PillowSpec {
	return []model.PillowSpec{{Name: "main", Mounts: []model.VolumeMount{{Volume: "workspace", Path: "/workspace"}}}}
}

// declareTestBed supplies the fixture's workload before waiting for readiness.
// Existing declarations are used unchanged by tests that configured their own Pillows.
func declareTestBed(m *manager.Manager, ctx context.Context, name string) (*manager.Resident, error) {
	if b, err := m.Ensure(ctx, name); !errors.Is(err, hostel.ErrNotFound) {
		return b, err
	}
	if _, err := m.InitializeBed(ctx, name, manager.CreateOptions{Pillows: testWorkspacePillows()}); err != nil {
		return nil, err
	}
	return m.Ensure(ctx, name)
}
