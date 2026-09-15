package view

import (
	"time"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
)

// Bed is the JSON shape for a bed in the management API.
type Bed struct {
	Sync         string        `json:"sync"`
	ID           string        `json:"id"`
	Status       bed.BedStatus `json:"status"`
	DataSynced   bool          `json:"data_synced"`
	Pinned       bool          `json:"pinned"`
	Workdir      string        `json:"workdir"`
	CreatedAt    time.Time     `json:"created_at,omitzero"`
	LastActiveAt time.Time     `json:"last_active_at,omitzero"`
	KeepaliveAt  time.Time     `json:"keepalive_at,omitzero"`
	RetainUntil  time.Time     `json:"retained_until,omitzero"`
}

type LifecycleStage struct {
	Name       string `json:"name"`
	Result     string `json:"result"`
	DurationMs int64  `json:"duration_ms"`
}

type LifecycleRecord struct {
	Action      string           `json:"action"`
	Result      string           `json:"result"`
	Source      string           `json:"source,omitempty"`
	Trigger     string           `json:"trigger,omitempty"`
	StartedAt   time.Time        `json:"started_at"`
	FinishedAt  time.Time        `json:"finished_at"`
	DurationMs  int64            `json:"duration_ms"`
	Stages      []LifecycleStage `json:"stages"`
	FailedStage string           `json:"failed_stage,omitempty"`
	Error       string           `json:"error,omitempty"`
}

type Lifecycle struct {
	LastInitialization *LifecycleRecord `json:"last_initialization,omitempty"`
	LastPersist        *LifecycleRecord `json:"last_persist,omitempty"`
}

// Activity is what the bed is doing right now, by request category
// (docs/kernel.md): operations are in-flight stateless requests, sessions
// are open stateful holds. Sessions never raise the bed's activity — an idle bed
// may still hold cdp connections.
type Activity struct {
	Operations map[bed.OperationKind]int `json:"operations,omitempty"`
	Sessions   map[bed.SessionKind]int   `json:"sessions,omitempty"`
}

type Executor struct {
	ID      string `json:"id"`
	Backend string `json:"backend"`
	State   string `json:"state"`
}

type BedDetail struct {
	Bed
	Status             BedStatus  `json:"status"`
	Generation         int64      `json:"generation"`
	SnapshotGeneration int64      `json:"snapshot_generation,omitempty"`
	SnapshotBytes      int64      `json:"snapshot_bytes,omitempty"`
	LocalBytes         int64      `json:"local_bytes,omitempty"`
	RestoreBytes       int64      `json:"restore_bytes,omitempty"`
	Activity           Activity   `json:"activity"`
	Lifecycle          *Lifecycle `json:"lifecycle,omitempty"`
	Executor           *Executor  `json:"executor,omitempty"`
}

func BedInitialization(status bed.InitializationStatus) Bed {
	return Bed{ID: status.ID, Sync: string(status.Sync), Workdir: bedfs.DefaultWorkdir, Status: status.BedStatus}
}

func ExecutorFromStatus(status *bed.ExecutorStatus) *Executor {
	if status == nil {
		return nil
	}
	return &Executor{ID: status.ID, Backend: status.Backend, State: string(status.State)}
}

func LifecycleRecordFrom(record *bed.LifecycleRecord) *LifecycleRecord {
	if record == nil {
		return nil
	}
	stages := make([]LifecycleStage, 0, len(record.Stages))
	for _, stage := range record.Stages {
		stages = append(stages, LifecycleStage{
			Name:       stage.Name,
			Result:     stage.Result,
			DurationMs: stage.Duration.Milliseconds(),
		})
	}
	return &LifecycleRecord{
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

type BedExpiration struct {
	ExpiresAt *time.Time `json:"expiresAt"`
}
