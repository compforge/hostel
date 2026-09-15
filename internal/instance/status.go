// Package instance composes host facts, Bed components and Amenity status.
package instance

import (
	"github.com/qiankunli/hostel/internal/amenity"
	"github.com/qiankunli/hostel/internal/bed"
	manager "github.com/qiankunli/hostel/internal/bed/manager"
	"github.com/qiankunli/hostel/internal/bed/service"
	"github.com/qiankunli/hostel/internal/host/facts"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

type Observer struct {
	host      facts.SystemFacts
	Beds      *manager.Manager
	Amenities *amenity.Manager
}

// NewObserver composes the same boot snapshot supplied to the domain managers.
// Construction and status reads never probe the host.
func NewObserver(host facts.Snapshot, beds *manager.Manager, amenities *amenity.Manager) Observer {
	return Observer{Beds: beds, Amenities: amenities, host: host.System}
}

// Status separates host observations from domain and facility status.
// Inventory contains summaries, never tenant details.
type Status struct {
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

func (o Observer) Status() Status {
	return Status{
		SchemaVersion: 6,
		Host:          Host{Fact: o.host, Status: HostStatus{Ports: o.Beds.PortStatus()}},
		Status:        o.Beds.Status(), Amenities: o.Amenities.Status(),
	}
}

// BedStatus uses the same component/amenity organization at unit granularity.
// Tenant observations are joined through bindings, not stored in bed.Status.
type BedStatus struct {
	Isolation  bed.RoomStatus    `json:"isolation"`
	Lifecycle  manager.BedStatus `json:"lifecycle"`
	Components struct {
		Filesystem bed.FilesystemStatus `json:"filesystem"`
		Privilege  bed.PrivilegeStatus  `json:"privilege"`
		Network    bed.NetworkStatus    `json:"network"`
		Store      bed.StoreStatus      `json:"store"`
		Executor   bed.ExecutorStatus   `json:"executor"`
		Resource   bed.ResourceStatus   `json:"resource"`
		Services   []service.Status     `json:"services"`
	} `json:"components"`
	Amenities map[string]amenity.BindingStatus `json:"amenities"`
}

func (o Observer) BedStatus(b *bed.Bed, lifecycle manager.BedStatus) BedStatus {
	report := BedStatus{Lifecycle: lifecycle, Isolation: o.Beds.RoomStatus(), Amenities: make(map[string]amenity.BindingStatus)}
	if b == nil {
		return report
	}
	report.Amenities = o.Amenities.BedStatus(b.ID)
	state := b.Status()
	report.Components.Filesystem = state.Filesystem
	report.Components.Privilege = state.Privilege
	report.Components.Network = state.Network
	report.Components.Store = state.Store
	report.Components.Executor = state.Executor
	report.Components.Resource = state.Resource
	report.Components.Services = o.Beds.Services().Status(b)
	return report
}
