package manager

import (
	"errors"
	model "github.com/qiankunli/hostel/internal/bed"
	"slices"
)

var ErrPortsConflict = errors.New("bed: port mapping declaration cannot change")

func checkBedPorts(options CreateOptions, actual model.Spec) error {
	if !options.lookup && !slices.Equal(options.PortMappings, actual.PortMappings) {
		return ErrPortsConflict
	}
	return nil
}
