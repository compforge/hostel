package manager

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	model "github.com/qiankunli/hostel/internal/bed"
)

func testWorkspaceVolume(policy string) []model.VolumeSpec {
	return []model.VolumeSpec{{Name: "workspace", Managed: &model.ManagedVolume{Persistence: policy}}}
}

func TestManagedVolumeContractSurvivesRestore(t *testing.T) {
	st := newFakeStore()
	m := storeTestManager(t, t.TempDir(), st)
	b, err := m.Ensure(t.Context(), "volumes")
	if err != nil {
		t.Fatal(err)
	}
	want := testWorkspaceVolume("sync")
	if !reflect.DeepEqual(b.Spec().Volumes, want) {
		t.Fatalf("volumes=%+v", b.Spec().Volumes)
	}
	if err := m.Checkpoint(t.Context(), b.Name); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	next := storeTestManager(t, t.TempDir(), st)
	defer next.Close(t.Context())
	restored, err := next.Ensure(t.Context(), "volumes")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Spec().Volumes, want) {
		t.Fatalf("restored=%+v", restored.Spec().Volumes)
	}
	if _, err := next.InitializeBedWithOptions(t.Context(), "volumes", CreateOptions{Volumes: testWorkspaceVolume("none")}); !errors.Is(err, ErrPathsConflict) {
		t.Fatalf("conflict=%v", err)
	}
}

func TestLocalIdentityKeepsHostPathsOutOfPortableMetadata(t *testing.T) {
	m := storeTestManager(t, t.TempDir(), newFakeStore())
	defer m.Close(t.Context())
	volumes := append(testWorkspaceVolume("sync"), model.VolumeSpec{Name: "external", HostPath: &model.HostPathVolume{Path: t.TempDir()}})
	if _, err := m.InitializeBedWithOptions(t.Context(), "paths", CreateOptions{Volumes: volumes}); err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(t.Context(), "paths")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Checkpoint(t.Context(), b.Name); err != nil {
		t.Fatal(err)
	}
	meta, ok := loadMeta(b.Spec().Dir)
	if !ok || !reflect.DeepEqual(meta.Volumes, testWorkspaceVolume("sync")) {
		t.Fatalf("portable=%+v", meta)
	}
	if _, err := os.Stat(filepath.Join(b.Spec().Dir, "rootfs")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(b.Spec().Dir, "volumes", "workspace")); err != nil {
		t.Fatal(err)
	}
}
