package view

import (
	"github.com/qiankunli/hostel/internal/amenity"
	manager "github.com/qiankunli/hostel/internal/bed/manager"
	"github.com/qiankunli/hostel/internal/host/facts"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

// StatusSchemaVersion identifies the instance diagnostics wire contract.
const StatusSchemaVersion = 11

// InstanceStatus separates host observations from domain and facility status.
// Inventory contains summaries, never tenant details.
type InstanceStatus struct {
	SchemaVersion int  `json:"schema_version"`
	Host          Host `json:"host"`
	manager.Status
	Amenities map[string]amenity.Status `json:"amenities"`
}

// Host separates the immutable boot snapshot from live resource observations.
// Neither branch performs probes during a status read.
type Host struct {
	Fact   facts.SystemFacts `json:"fact"`
	Status HostStatus        `json:"status"`
}

type HostStatus struct {
	// Ports contains Hostel-managed allocations, not every OS listener.
	Ports []hostnetwork.PortStatus `json:"ports"`
}
