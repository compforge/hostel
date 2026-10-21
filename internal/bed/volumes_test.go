package bed

import (
	"errors"
	"testing"
)

func TestVolumeDeclarationValidation(t *testing.T) {
	for _, name := range []string{"../escape", "", "a/b", ".", ".."} {
		if _, err := NormalizeVolumes([]VolumeSpec{{Name: name, Managed: &ManagedVolume{Persistence: "none"}}}); !errors.Is(err, ErrInvalidPaths) {
			t.Fatalf("name=%q error=%v", name, err)
		}
	}
	if _, err := NormalizeVolumes([]VolumeSpec{{Name: "v"}}); err == nil {
		t.Fatal("missing volume source accepted")
	}
	for _, mounts := range [][]VolumeMount{
		{{Volume: "v", Path: "/proc/private"}},
		{{Volume: "v", Path: "/a"}, {Volume: "v", Path: "/b"}},
		{{Volume: "v", Path: "/a"}, {Volume: "w", Path: "/a/child"}},
	} {
		if _, err := NormalizeMounts(mounts); err == nil {
			t.Fatalf("invalid mounts accepted: %+v", mounts)
		}
	}
	spec := Spec{Volumes: []VolumeSpec{{Name: "v", Managed: &ManagedVolume{Persistence: "none"}}}, Pillows: append([]PillowSpec{{Name: "workspace", Mounts: []VolumeMount{{Volume: "v", Path: "/run/bootstrap", ReadOnly: true}}}}, []PillowSpec{{Name: "writer", Mounts: []VolumeMount{{Volume: "v", Path: "/run/bootstrap"}}}}...)}
	if err := ValidateVolumeMounts(spec); err != nil {
		t.Fatal(err)
	}
	spec.Pillows[0].Mounts[0].Volume = "unknown"
	if err := ValidateVolumeMounts(spec); err == nil {
		t.Fatal("unknown pillow volume accepted")
	}
}

func TestHostVolumeSources(t *testing.T) {
	for _, sources := range [][]VolumeSpec{
		{{Name: "v", HostPath: &HostPathVolume{Path: "relative"}}},
		{{Name: "v", HostPath: &HostPathVolume{Path: "/"}}},
		{{Name: "v", HostPath: &HostPathVolume{Path: "/data"}, Managed: &ManagedVolume{Persistence: "none"}}},
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

func TestManagedPersistencePolicies(t *testing.T) {
	for _, policy := range []string{"", "auto", "cas", "invalid"} {
		if _, err := NormalizeVolumes([]VolumeSpec{{Name: "v", Managed: &ManagedVolume{Persistence: policy}}}); err == nil {
			t.Fatalf("accepted persistence %q", policy)
		}
	}
	in := []VolumeSpec{{Name: "v", Managed: &ManagedVolume{Persistence: "sync"}}}
	out, err := NormalizeVolumes(in)
	if err != nil {
		t.Fatal(err)
	}
	out[0].Managed.Persistence = "none"
	if in[0].Managed.Persistence != "sync" {
		t.Fatal("normalization aliases caller policy")
	}
}
