package isolation

import (
	"github.com/qiankunli/hostel/internal/feature"
	"github.com/qiankunli/hostel/internal/host/facts"
)

// DiagnosticsReport retains the filesystem resolver's immutable boot evidence.
// Reading status never runs a probe.
type DiagnosticsReport struct {
	Features map[string]feature.Status    `json:"features"`
	Probes   map[string]facts.ProbeReport `json:"probes"`
}
