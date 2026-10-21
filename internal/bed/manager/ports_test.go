package manager

import (
	"errors"
	"testing"

	model "github.com/qiankunli/hostel/internal/bed"
)

func TestPortMappingDeclarationIsImmutable(t *testing.T) {
	m, specs, _ := testPillowManager(t)
	options := testPillowOptions(specs[:1])
	if _, err := m.InitializeBed(t.Context(), "mapping", options); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(t.Context(), "mapping"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.InitializeBed(t.Context(), "mapping", options); err != nil {
		t.Fatal(err)
	}
	options.PortMappings[0].BedPort = 8080
	if _, err := m.InitializeBed(t.Context(), "mapping", options); !errors.Is(err, ErrPortsConflict) {
		t.Fatalf("changed declaration accepted: %v", err)
	}
	// No consumer still means a declared network demand, not permission to alter it.
	actual := model.Spec{PortMappings: []model.PortMappingSpec{{Name: "http", Protocol: "tcp"}}}
	if !errors.Is(checkBedPorts(CreateOptions{Pillows: testWorkspacePillows()}, actual), ErrPortsConflict) {
		t.Fatal("omitted declaration silently removed mapping")
	}
}
