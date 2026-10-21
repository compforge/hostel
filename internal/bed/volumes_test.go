package bed

import (
	"errors"
	"testing"
)

func TestVolumeDeclarationValidation(t *testing.T) {
	for _, name := range []string{"../escape", "", "a/b", ".", ".."} {
		if _, err := NormalizeVolumes([]VolumeSpec{{Name: name, EmptyDir: &EmptyDirVolume{}}}); !errors.Is(err, ErrInvalidPaths) {
			t.Fatalf("name=%q error=%v", name, err)
		}
	}
	if _, err := NormalizeVolumes([]VolumeSpec{{Name: "v"}}); err == nil {
		t.Fatal("missing volume source accepted")
	}
	for _, mounts := range [][]VolumeMount{
		{{Volume: "v", Path: "/workspace/private"}}, {{Volume: "v", Path: "/proc/private"}},
		{{Volume: "v", Path: "/a"}, {Volume: "v", Path: "/b"}},
		{{Volume: "v", Path: "/a"}, {Volume: "w", Path: "/a/child"}},
	} {
		if _, err := NormalizeMounts(mounts); err == nil {
			t.Fatalf("invalid mounts accepted: %+v", mounts)
		}
	}
	spec := Spec{Volumes: []VolumeSpec{{Name: "v", EmptyDir: &EmptyDirVolume{}}}, Pillows: append([]PillowSpec{{Name: "workspace", Mounts: []VolumeMount{{Volume: "v", Path: "/run/bootstrap", ReadOnly: true}}}}, []PillowSpec{{Name: "writer", Mounts: []VolumeMount{{Volume: "v", Path: "/run/bootstrap"}}}}...)}
	if err := ValidateVolumeMounts(spec); err != nil {
		t.Fatal(err)
	}
	spec.SyncPaths = []string{"/run"}
	if err := ValidateVolumeMounts(spec); err == nil {
		t.Fatal("volume included in snapshot")
	}
	spec.SyncPaths = nil
	spec.Pillows[0].Mounts[0].Volume = "unknown"
	if err := ValidateVolumeMounts(spec); err == nil {
		t.Fatal("unknown pillow volume accepted")
	}
}

func TestHostVolumeSources(t *testing.T) {
	for _, sources := range [][]VolumeSpec{
		{{Name: "v", HostPath: &HostPathVolume{Path: "relative"}}},
		{{Name: "v", HostPath: &HostPathVolume{Path: "/"}}},
		{{Name: "v", HostPath: &HostPathVolume{Path: "/data"}, EmptyDir: &EmptyDirVolume{}}},
		{{Name: "a", HostPath: &HostPathVolume{Path: "/data"}}, {Name: "b", HostPath: &HostPathVolume{Path: "/data/child"}}},
	} {
		if _, err := NormalizeVolumes(sources); !errors.Is(err, ErrInvalidPaths) {
			t.Fatalf("sources accepted: %+v %v", sources, err)
		}
	}
	input := []VolumeSpec{{Name: "v", HostPath: &HostPathVolume{Path: "/data/../source"}}}
	got, err := NormalizeVolumes(input)
	if err != nil || got[0].HostPath.Path != "/source" {
		t.Fatalf("normalize: %+v %v", got, err)
	}
	got[0].HostPath.Path = "changed"
	if input[0].HostPath.Path != "/data/../source" {
		t.Fatal("normalization mutated caller source")
	}
}
