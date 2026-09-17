package service

import (
	"fmt"
	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/network"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

func newTestManager(ports *hostnetwork.PortManager, changed func(*bed.Bed)) *Manager {
	return NewManager(network.NewPortMappings(ports), changed)
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

func testServiceURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}
