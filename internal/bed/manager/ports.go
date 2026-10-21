package manager

import (
	"errors"
	"slices"

	model "github.com/qiankunli/hostel/internal/bed"
)

var ErrPortsConflict = errors.New("bed: port mapping declaration cannot change")

func checkBedPorts(options CreateOptions, actual model.Spec) error {
	if !slices.Equal(options.PortMappings, actual.PortMappings) {
		return ErrPortsConflict
	}
	return nil
}
