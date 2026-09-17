package service

import (
	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/network"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

func newTestManager(ports *hostnetwork.PortManager, advertise string, changed func(*bed.Bed)) *Manager {
	return NewManager(network.NewPortMappings(ports, advertise), changed)
}
func newServiceTestBed(name string, id bed.ID, spec bed.Spec) *bed.Bed {
	spec.Services = bed.CloneServices(spec.Services)
	for i := range spec.Services {
		s := &spec.Services[i]
		if s.HTTP != nil {
			s.PortMapping = s.Name
			spec.PortMappings = append(spec.PortMappings, bed.PortMappingSpec{Name: s.Name, Protocol: "tcp", Publish: true})
		}
	}
	return bed.New(name, id, spec)
}
