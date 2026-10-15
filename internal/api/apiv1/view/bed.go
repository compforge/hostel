package view

import (
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	"github.com/qiankunli/hostel/protocol"

	bed "github.com/qiankunli/hostel/internal/bed/manager"
)

func BedInitialization(status bed.InitializationStatus) protocol.Bed {
	return protocol.Bed{ID: status.ID, Name: status.Name, Sync: string(status.Sync), Workdir: bedfs.DefaultWorkdir, Status: status.BedStatus}
}

func ExecutorFromStatus(status *bed.ExecutorStatus) *protocol.Executor {
	if status == nil {
		return nil
	}
	return &protocol.Executor{ID: status.ID, Backend: status.Backend, PrivatePIDNamespace: status.PrivatePIDNamespace, State: string(status.State)}
}

func LifecycleRecordFrom(record *bed.LifecycleRecord) *protocol.LifecycleRecord {
	if record == nil {
		return nil
	}
	stages := make([]protocol.LifecycleStage, 0, len(record.Stages))
	for _, stage := range record.Stages {
		stages = append(stages, protocol.LifecycleStage{
			Name:       stage.Name,
			Result:     stage.Result,
			DurationMs: stage.Duration.Milliseconds(),
		})
	}
	return &protocol.LifecycleRecord{
		Action:      record.Action,
		Result:      record.Result,
		Source:      record.Source,
		Trigger:     record.Trigger,
		StartedAt:   record.StartedAt,
		FinishedAt:  record.FinishedAt,
		DurationMs:  record.Duration.Milliseconds(),
		Stages:      stages,
		FailedStage: record.FailedStage,
		Error:       record.Error,
	}
}
