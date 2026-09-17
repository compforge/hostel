package manager

import (
	"errors"
	model "github.com/qiankunli/hostel/internal/bed"
	"testing"
)

func TestPortMappingDeclarationIsImmutable(t *testing.T) {
	m, specs, _ := testServiceManager(t)
	options := testServiceOptions(specs[:1])
	if _, err := m.InitializeBedWithOptions(t.Context(), "mapping", options); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(t.Context(), "mapping"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.InitializeBedWithOptions(t.Context(), "mapping", options); err != nil {
		t.Fatal(err)
	}
	options.PortMappings[0].BedPort = 8080
	if _, err := m.InitializeBedWithOptions(t.Context(), "mapping", options); !errors.Is(err, ErrPortsConflict) {
		t.Fatalf("changed declaration accepted: %v", err)
	}
	// No consumer still means a declared network demand, not permission to alter it.
	actual := model.Spec{PortMappings: []model.PortMappingSpec{{Name: "http", Protocol: "tcp"}}}
	if !errors.Is(checkBedPorts(CreateOptions{}, actual), ErrPortsConflict) {
		t.Fatal("omitted declaration silently removed mapping")
	}
	if err := checkBedPorts(CreateOptions{lookup: true}, actual); err != nil {
		t.Fatal(err)
	}
}
