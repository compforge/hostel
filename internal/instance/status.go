// Package instance composes host facts, Bed components and Amenity status.
package instance

import (
	"github.com/qiankunli/hostel/internal/amenity"
	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	manager "github.com/qiankunli/hostel/internal/bed/manager"
	"github.com/qiankunli/hostel/internal/host/facts"
)

type Observer struct {
	host      facts.SystemFacts
	Beds      *manager.Manager
	Amenities *amenity.Manager
}

// NewObserver reuses the host snapshot captured by the filesystem resolver.
// Embedded runtimes may supply an isolator without a report; collect once at
// assembly in that case, never on a status request.
func NewObserver(beds *manager.Manager, amenities *amenity.Manager) Observer {
	var host facts.SystemFacts
	if report, ok := beds.Isolator().(isolation.Report); ok {
		host = report.Facts().System
	} else {
		host = facts.Collect().System
	}
	return Observer{Beds: beds, Amenities: amenities, host: host}
}

// Status separates host observations from domain and facility status.
// Inventory contains summaries, never tenant details.
type Status struct {
	SchemaVersion int               `json:"schema_version"`
	Host          facts.SystemFacts `json:"host"`
	manager.Status
	Amenities map[string]amenity.Status `json:"amenities"`
}

func (o Observer) Status() Status {
	return Status{SchemaVersion: 2, Host: o.host, Status: o.Beds.Status(), Amenities: o.Amenities.Status()}
}

// BedStatus uses the same component/amenity organization at unit granularity.
// Tenant observations are joined through bindings, not stored in bed.Status.
type BedStatus struct {
	Lifecycle  manager.BedStatus `json:"lifecycle"`
	Components struct {
		Filesystem bed.FilesystemStatus `json:"filesystem"`
		Privilege  bed.PrivilegeStatus  `json:"privilege"`
		Network    bed.NetworkStatus    `json:"network"`
		Store      bed.StoreStatus      `json:"store"`
		Executor   bed.ExecutorStatus   `json:"executor"`
		Resource   bed.ResourceStatus   `json:"resource"`
	} `json:"components"`
	Amenities map[string]amenity.BindingStatus `json:"amenities"`
}

func (o Observer) BedStatus(b *bed.Bed, lifecycle manager.BedStatus) BedStatus {
	report := BedStatus{Lifecycle: lifecycle, Amenities: make(map[string]amenity.BindingStatus)}
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
	return report
}
