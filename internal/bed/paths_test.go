package bed

import (
	"errors"
	"testing"
)

func TestPathDeclarationValidationAndCloning(t *testing.T) {
	for _, paths := range [][]string{{"/"}, {"/data", "/data/sub"}} {
		if _, err := NormalizeSyncPaths(paths); !errors.Is(err, ErrInvalidPaths) {
			t.Fatalf("accepted %v: %v", paths, err)
		}
	}
	for _, mounts := range [][]VolumeMount{{{Volume: "v", Path: "/workspace/x"}}, {{Volume: "v", Path: "relative"}}, {{Volume: "a", Path: "/project"}, {Volume: "b", Path: "/project/sub"}}} {
		if _, err := NormalizeMounts(mounts); !errors.Is(err, ErrInvalidPaths) {
			t.Fatalf("accepted mounts %v: %v", mounts, err)
		}
	}
	for _, paths := range [][]string{nil, {}} {
		b := New("bed", "", Spec{SyncPaths: paths})
		if (b.Spec().SyncPaths == nil) != (paths == nil) {
			t.Fatal("lost nil/empty distinction")
		}
	}
	paths := []string{"/memory", "/workspace"}
	volumes := []VolumeSpec{{Name: "external", HostPath: &HostPathVolume{Path: "/volume"}}}
	b := New("bed", "", Spec{SyncPaths: paths, Volumes: volumes})
	paths[0] = "/changed"
	volumes[0].HostPath.Path = "/changed"
	spec := b.Spec()
	spec.SyncPaths[0] = "/changed"
	spec.Volumes[0].HostPath.Path = "/changed"
	if b.Spec().SyncPaths[0] != "/memory" || b.Spec().Volumes[0].HostPath.Path != "/volume" {
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
