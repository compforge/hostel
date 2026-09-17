package handler

import (
	"github.com/qiankunli/hostel/internal/amenity"
	apiview "github.com/qiankunli/hostel/internal/api/apiv1/view"
	"github.com/qiankunli/hostel/internal/bed"
	manager "github.com/qiankunli/hostel/internal/bed/manager"
	"github.com/qiankunli/hostel/internal/host/facts"
)

// statusObserver composes API views from domain-owned snapshots; it owns no
// lifecycle state and never probes the host during construction or reads.
type statusObserver struct {
	host      facts.SystemFacts
	beds      *manager.Manager
	amenities *amenity.Manager
}

// newStatusObserver uses the same boot snapshot supplied to the domain managers.
// Construction and status reads never probe the host.
func newStatusObserver(host facts.Snapshot, beds *manager.Manager, amenities *amenity.Manager) statusObserver {
	return statusObserver{beds: beds, amenities: amenities, host: host.System}
}

func (o statusObserver) Status() apiview.InstanceStatus {
	return apiview.InstanceStatus{
		SchemaVersion: apiview.StatusSchemaVersion,
		Host:          apiview.Host{Fact: o.host, Status: apiview.HostStatus{Ports: o.beds.PortStatus()}},
		Status:        o.beds.Status(), Amenities: o.amenities.Status(),
	}
}

func (o statusObserver) BedStatus(b *bed.Bed, lifecycle manager.BedStatus) apiview.BedStatus {
	report := apiview.BedStatus{Lifecycle: lifecycle, Isolation: o.beds.RoomStatus(), Amenities: make(map[string]amenity.BindingStatus)}
	if b == nil {
		return report
	}
	report.Amenities = o.amenities.BedStatus(b.ID)
	state := b.Status()
	report.Components.Filesystem = state.Filesystem
	report.Components.Privilege = state.Privilege
	report.Components.Network = state.Network
	report.Components.Store = state.Store
	report.Components.Executor = state.Executor
	report.Components.Resource = state.Resource
	report.Components.Services = o.beds.Services().Status(b)
	return report
}
