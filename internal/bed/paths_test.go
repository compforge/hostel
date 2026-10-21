package bed

import (
	"errors"
	"testing"
)

func TestPathDeclarationValidationAndCloning(t *testing.T) {
	for _, mounts := range [][]VolumeMount{{{Volume: "v", Path: "/proc/x"}}, {{Volume: "v", Path: "relative"}}, {{Volume: "a", Path: "/project"}, {Volume: "b", Path: "/project/sub"}}} {
		if _, err := NormalizeMounts(mounts); !errors.Is(err, ErrInvalidPaths) {
			t.Fatalf("accepted mounts %v: %v", mounts, err)
		}
	}
	volumes := []VolumeSpec{{Name: "external", HostPath: &HostPathVolume{Path: "/volume"}}}
	b := New("bed", "", Spec{Volumes: volumes})
	volumes[0].HostPath.Path = "/changed"
	spec := b.Spec()
	spec.Volumes[0].HostPath.Path = "/changed"
	if b.Spec().Volumes[0].HostPath.Path != "/volume" {
		t.Fatal("spec aliases caller state")
	}
}

func TestTemporaryDataMappingHasNoInternalMountReservation(t *testing.T) {
	for _, target := range []string{"/tmp", "/tmp/.hostel"} {
		if _, err := NormalizeMounts([]VolumeMount{{Volume: "volume", Path: target}}); err != nil {
			t.Fatalf("mapping ordinary Bed data %s: %v", target, err)
		}
	}
}
