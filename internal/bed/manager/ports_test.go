package manager

import (
	"errors"
	"testing"
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
	options.Pillows[0].Ports[0].Publish = false
	if _, err := m.InitializeBed(t.Context(), "mapping", options); !errors.Is(err, ErrPillowsConflict) {
		t.Fatalf("changed declaration accepted: %v", err)
	}
}
