package pillow

import (
	"fmt"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/network"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

func newTestManager(ports *hostnetwork.PortManager, changed func(*bed.Bed)) *Manager {
	return NewManager(network.NewPortMappings(ports), changed)
}
func newPillowTestBed(name string, id bed.ID, spec bed.Spec) *bed.Bed {
	spec.Pillows = bed.ClonePillows(spec.Pillows)
	for i := range spec.Pillows {
		s := &spec.Pillows[i]
		if s.HTTP != nil {
			s.PortMapping = s.Name
			spec.PortMappings = append(spec.PortMappings, bed.PortMappingSpec{Name: s.Name, Protocol: "tcp", Publish: true})
		}
	}
	return bed.New(name, id, spec)
}

func testPillowURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}
