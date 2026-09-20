package view

import (
	"github.com/qiankunli/hostel/internal/amenity"
	"github.com/qiankunli/hostel/internal/bed"
	manager "github.com/qiankunli/hostel/internal/bed/manager"
	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/bed/service"
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

// BedStatus uses the same component/amenity organization at unit granularity.
// Tenant observations are joined through bindings, not stored in bed.Status.
type BedStatus struct {
	Services     []service.Status            `json:"services"`
	PortMappings []network.PortMappingStatus `json:"port_mappings"`
	Isolation    bed.RoomStatus              `json:"isolation"`
	Lifecycle    manager.BedStatus           `json:"lifecycle"`
	Components   struct {
		Filesystem bed.FilesystemStatus `json:"filesystem"`
		Privilege  bed.PrivilegeStatus  `json:"privilege"`
		Network    bed.NetworkStatus    `json:"network"`
		Store      bed.StoreStatus      `json:"store"`
		Executor   bed.ExecutorStatus   `json:"executor"`
		Resource   bed.ResourceStatus   `json:"resource"`
	} `json:"components"`
	Amenities map[string]amenity.BindingStatus `json:"amenities"`
}
