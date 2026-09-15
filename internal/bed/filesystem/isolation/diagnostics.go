package isolation

import (
	"github.com/qiankunli/hostel/internal/bed/tool"
	"github.com/qiankunli/hostel/internal/host/facts"
)

// DiagnosticsReport retains the filesystem resolver's immutable boot evidence.
// Reading status never runs a probe.
type DiagnosticsReport struct {
	Tools  map[string]tool.Status       `json:"tools"`
	Probes map[string]facts.ProbeReport `json:"probes"`
}
