package manager

import (
	"context"
	"encoding/json"
	"errors"
	hostel "github.com/qiankunli/hostel/internal"
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
	b, err := declareTestBed(m, t.Context(), "volumes")
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
	restored, err := declareTestBed(next, t.Context(), "volumes")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Spec().Volumes, want) {
		t.Fatalf("restored=%+v", restored.Spec().Volumes)
	}
	if _, err := next.InitializeBed(t.Context(), "volumes", CreateOptions{Volumes: testWorkspaceVolume("none"), Pillows: testWorkspacePillows()}); !errors.Is(err, ErrPathsConflict) {
		t.Fatalf("conflict=%v", err)
	}
}

func TestLocalIdentityKeepsHostPathsOutOfPortableMetadata(t *testing.T) {
	m := storeTestManager(t, t.TempDir(), newFakeStore())
	defer m.Close(t.Context())
	volumes := append(testWorkspaceVolume("sync"), model.VolumeSpec{Name: "external", HostPath: &model.HostPathVolume{Path: t.TempDir()}})
	if _, err := m.InitializeBed(t.Context(), "paths", CreateOptions{Volumes: volumes, Pillows: testWorkspacePillows()}); err != nil {
		t.Fatal(err)
	}
	b, err := declareTestBed(m, t.Context(), "paths")
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

// dataVolumeStore extends the existing test backend to persist the custom data volume.
type dataVolumeStore struct{ *fakeStore }

func (s *dataVolumeStore) Persist(ctx context.Context, id, dir string, generation int64, roots []string) error {
	if err := s.fakeStore.Persist(ctx, id, dir, generation, roots); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(dir, "volumes", "data", "marker.txt"))
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.snaps[id] = data
	s.mu.Unlock()
	return nil
}
func (s *dataVolumeStore) Restore(_ context.Context, id, dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(dir, "volumes", "data"), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), s.metas[id], 0644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "volumes", "data", "marker.txt"), s.snaps[id], 0644)
}
func TestCustomVolumeRestoreRequiresPillowDeclaration(t *testing.T) {
	st := &dataVolumeStore{newFakeStore()}
	options := CreateOptions{
		Volumes: []model.VolumeSpec{{Name: "data", Managed: &model.ManagedVolume{Persistence: "sync"}}},
		Pillows: []model.PillowSpec{{Name: "main", Mounts: []model.VolumeMount{{Volume: "data", Path: "/data"}}}},
	}
	first := storeTestManager(t, t.TempDir(), st)
	defer first.Close(t.Context())
	if _, err := first.InitializeBed(t.Context(), "custom", options); err != nil {
		t.Fatal(err)
	}
	b, err := first.Ensure(t.Context(), "custom")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.Spec().Dir, "volumes", "data", "marker.txt"), []byte("durable marker"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := first.Checkpoint(t.Context(), "custom"); err != nil {
		t.Fatal(err)
	}
	if ok, err := first.Evict(t.Context(), "custom"); err != nil || !ok {
		t.Fatalf("evict %v %v", ok, err)
	}
	t.Run("access_does_not_restore", func(t *testing.T) {
		next := storeTestManager(t, t.TempDir(), st)
		defer next.Close(t.Context())
		if _, err := next.Ensure(t.Context(), "custom"); !errors.Is(err, hostel.ErrNotFound) {
			t.Fatalf("access = %v", err)
		}
		if _, err := os.Stat(filepath.Join(next.root, "custom")); !os.IsNotExist(err) {
			t.Fatalf("access created data: %v", err)
		}
	})
	t.Run("explicit_declarations_restore_data", func(t *testing.T) {
		next := storeTestManager(t, t.TempDir(), st)
		defer next.Close(t.Context())
		restore := CreateOptions{Pillows: model.ClonePillows(options.Pillows)}
		restore.Pillows[0].Mounts[0].Path = "/restored"
		if _, err := next.InitializeBed(t.Context(), "custom", restore); err != nil {
			t.Fatal(err)
		}
		b, err := next.Ensure(t.Context(), "custom")
		if err != nil {
			t.Fatal(err)
		}
		path, err := b.BedFS().Resolve("/restored/marker.txt")
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if len(b.Spec().Pillows) != 1 || !reflect.DeepEqual(b.Spec().Volumes, options.Volumes) {
			t.Fatalf("invented declaration: %+v", b.Spec())
		}
		if err != nil || string(data) != "durable marker" {
			t.Fatalf("restored=%q err=%v", data, err)
		}
	})
}

// +case:id=temporary_volumes_after_restore,desc=`Restore persistent data with a changed temporary-volume declaration from a snapshot`,expect=`Persistent data survives, temporary volumes are empty and only the new declaration is used`
func TestRestoreUsesCurrentTemporaryVolumes(t *testing.T) {
	st := &dataVolumeStore{newFakeStore()}
	first := storeTestManager(t, t.TempDir(), st)
	defer first.Close(t.Context())
	durable := model.VolumeSpec{Name: "data", Managed: &model.ManagedVolume{Persistence: "sync"}}
	temporary := model.VolumeSpec{Name: "exchange", Managed: &model.ManagedVolume{Persistence: "none"}}
	options := CreateOptions{Volumes: []model.VolumeSpec{durable, temporary}, Pillows: []model.PillowSpec{{Name: "main", Mounts: []model.VolumeMount{{Volume: "data", Path: "/data"}, {Volume: "exchange", Path: "/exchange"}}}}}
	if _, err := first.InitializeBed(t.Context(), "custom", options); err != nil {
		t.Fatal(err)
	}
	b, err := first.Ensure(t.Context(), "custom")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"data", "exchange"} {
		if err := os.WriteFile(filepath.Join(b.Spec().Dir, "volumes", name, "marker.txt"), []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := first.Evict(t.Context(), "custom"); err != nil || !ok {
		t.Fatalf("evict: %v %v", ok, err)
	}
	var meta bedMeta
	if err := json.Unmarshal(st.metas["custom"], &meta); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(meta.Volumes, []model.VolumeSpec{durable}) {
		t.Fatalf("snapshot includes temporary declaration: %+v", meta.Volumes)
	}
	for _, scenario := range []struct {
		name        string
		temporary   string
		omitVolumes bool
	}{
		{name: "recreate", temporary: "exchange"},
		{name: "replace", temporary: "replacement"},
		{name: "remove"},
		{name: "omit", omitVolumes: true},
	} {
		name := scenario.temporary
		t.Run(scenario.name, func(t *testing.T) {
			next := storeTestManager(t, t.TempDir(), st)
			defer next.Close(t.Context())
			restore := CreateOptions{Volumes: []model.VolumeSpec{durable}, Pillows: []model.PillowSpec{{Name: "main", Mounts: []model.VolumeMount{{Volume: "data", Path: "/data"}}}}}
			if name != "" {
				restore.Volumes = append(restore.Volumes, model.VolumeSpec{Name: name, Managed: &model.ManagedVolume{Persistence: "none"}})
				restore.Pillows[0].Mounts = append(restore.Pillows[0].Mounts, model.VolumeMount{Volume: name, Path: "/scratch"})
			}
			if scenario.omitVolumes {
				restore.Volumes = nil
			}
			if _, err := next.InitializeBed(t.Context(), "custom", restore); err != nil {
				t.Fatal(err)
			}
			restored, err := next.Ensure(t.Context(), "custom")
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(restored.Spec().Dir, "volumes", "data", "marker.txt"))
			if err != nil || string(data) != "data" {
				t.Fatalf("persistent data=%q err=%v", data, err)
			}
			if name != "" {
				entries, err := os.ReadDir(filepath.Join(restored.Spec().Dir, "volumes", name))
				if err != nil || len(entries) != 0 {
					t.Fatalf("temporary contents=%v err=%v", entries, err)
				}
			}
			if name != "exchange" {
				if _, err := os.Stat(filepath.Join(restored.Spec().Dir, "volumes", "exchange")); !os.IsNotExist(err) {
					t.Fatalf("obsolete temporary volume exists: %v", err)
				}
			}
			changed := restore
			changed.Volumes = append(model.CloneVolumes(restored.Spec().Volumes), model.VolumeSpec{Name: "live-change", Managed: &model.ManagedVolume{Persistence: "none"}})
			if _, err := next.InitializeBed(t.Context(), "custom", changed); !errors.Is(err, ErrPathsConflict) {
				t.Fatalf("live declaration change=%v", err)
			}
		})
	}
}
