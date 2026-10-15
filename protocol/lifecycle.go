package protocol

import (
	"time"
)

// LifecyclePhase is the coarse lifecycle of a local Bed identity.
type LifecyclePhase string

// Readiness reports data-plane availability. Reason is machine-readable;
// Message and UpdatedAt provide diagnostic context.
type Readiness struct {
	Ready     bool      `json:"status"`
	Reason    string    `json:"reason"`
	Message   string    `json:"message,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// OperationActivity counts admitted operations; open sessions do not make a Bed active.
type OperationActivity string

// BedLifecycle separates lifecycle phase, data-plane readiness and operation activity.
type BedLifecycle struct {
	Phase     LifecyclePhase    `json:"phase"`
	Readiness Readiness         `json:"readiness"`
	Activity  OperationActivity `json:"activity,omitempty"`
}

// OperationKind classifies stateless Bed operations.
type OperationKind string

// SessionKind classifies revocable stateful Bed holds.
type SessionKind string

const (
	PhaseInitializing LifecyclePhase = "initializing"
	PhaseResident     LifecyclePhase = "resident"
	PhaseEvicting     LifecyclePhase = "evicting"
	PhasePurging      LifecyclePhase = "purging"
	PhaseDormant      LifecyclePhase = "dormant"
	PhaseFailed       LifecyclePhase = "failed"
)

const (
	ActivityActive OperationActivity = "active"
	ActivityIdle   OperationActivity = "idle"
)

const (
	OpExec       OperationKind = "exec"
	OpFile       OperationKind = "file"
	OpBrowser    OperationKind = "browser"
	OpMCP        OperationKind = "mcp"
	OpCheckpoint OperationKind = "checkpoint"
	OpControl    OperationKind = "control"
	OpService    OperationKind = "service"
)

const (
	SessionKindShell SessionKind = "shell"
	SessionKindCDP   SessionKind = "cdp"
)
