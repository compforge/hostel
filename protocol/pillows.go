package protocol

import (
	"encoding/json"
)

// PillowStatus references a named PortMapping; the mapping owns allocated ports.
type PillowStatus struct {
	Ports    []string `json:"ports,omitempty"`
	Prepared bool     `json:"prepared"`
	Name     string   `json:"name"`
	Required bool     `json:"required"`
	Phase    string   `json:"phase"`
	// Ready is current availability, not a process-lifecycle phase.
	Ready       bool   `json:"ready"`
	ExecutionID string `json:"execution_id,omitempty"`
	ExecutorID  string `json:"executor_id,omitempty"`
	Restarts    int    `json:"restarts"`
	Reason      string `json:"reason,omitempty"`
	// Outcome preserves the existing process diagnostic JSON, without exporting
	// executor implementation types. It does not carry private transport detail.
	Outcome  json.RawMessage     `json:"outcome,omitempty"`
	Listener *ListenerInspection `json:"listener,omitempty"`
}
