package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/qiankunli/hostel/internal/bed"
)

func TestVolumeContractOnlyValidatesPersistentDeclarations(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"version":2,"volumes":[{"name":"data","managed":{"persistence":"sync"}}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	durable := bed.VolumeSpec{Name: "data", Managed: &bed.ManagedVolume{Persistence: "sync"}}
	volumes, exists, err := ReadVolumeContract(dir)
	if err != nil || !exists || !reflect.DeepEqual(volumes, []bed.VolumeSpec{durable}) {
		t.Fatalf("contract=%+v exists=%t err=%v", volumes, exists, err)
	}
	if err := validateRestoredVolumes(dir, nil); err == nil {
		t.Fatal("missing persistent directory accepted")
	}
	if err := os.MkdirAll(filepath.Join(dir, "volumes", "data"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, requested := range [][]bed.VolumeSpec{nil, {durable}, {durable, {Name: "new-scratch", Managed: &bed.ManagedVolume{Persistence: "none"}}}} {
		if err := validateRestoredVolumes(dir, requested); err != nil {
			t.Fatalf("temporary declaration rejected: %v", err)
		}
	}
	for _, requested := range [][]bed.VolumeSpec{{}, {{Name: "data", Managed: &bed.ManagedVolume{Persistence: "none"}}}, {{Name: "renamed", Managed: &bed.ManagedVolume{Persistence: "sync"}}}, {durable, {Name: "extra", Managed: &bed.ManagedVolume{Persistence: "sync"}}}} {
		if err := validateRestoredVolumes(dir, requested); err == nil {
			t.Fatalf("persistent declaration change accepted: %+v", requested)
		}
	}
}
