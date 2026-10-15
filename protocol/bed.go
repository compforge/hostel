package protocol

import (
	"time"
)

// ID identifies a Hostel-owned local Bed lifetime.
type ID string

func (id ID) String() string { return string(id) }

// Bed is the management API summary, sharing Hostel's identity semantics.
// Callers identify and address a Bed by Name. Hostel generates ID so caller
// identifiers cannot collide with internal resource ownership. A name may refer
// to different lifetimes after destruction and recreation; recovery with retained
// local data preserves ID, while recreation after Forget gets a new ID.
// +spec=`Name is caller-owned; ID uniquely identifies a local Bed lifetime and is never replaced by Name in responses.`
// +link:doc=docs/protocol.md
type Bed struct {
	Sync         string       `json:"sync"`
	ID           ID           `json:"id"`
	Name         string       `json:"name"`
	Status       BedLifecycle `json:"status"`
	DataSynced   bool         `json:"data_synced"`
	Pinned       bool         `json:"pinned"`
	Workdir      string       `json:"workdir"`
	CreatedAt    time.Time    `json:"created_at,omitzero"`
	LastActiveAt time.Time    `json:"last_active_at,omitzero"`
	KeepaliveAt  time.Time    `json:"keepalive_at,omitzero"`
	RetainUntil  time.Time    `json:"retained_until,omitzero"`
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

// Activity reports in-flight operations and open sessions independently.
type Activity struct {
	Operations map[OperationKind]int `json:"operations,omitempty"`
	Sessions   map[SessionKind]int   `json:"sessions,omitempty"`
}

// Executor identifies the current replaceable process realm.
type Executor struct {
	PrivatePIDNamespace bool   `json:"private_pid_namespace"`
	ID                  string `json:"id"`
	Backend             string `json:"backend"`
	State               string `json:"state"`
}

// BedDetail adds component observations and data versions to a Bed summary.
// Its status replaces the summary status with the complete BedStatus object.
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

type BedExpiration struct {
	ExpiresAt *time.Time `json:"expiresAt"`
}
