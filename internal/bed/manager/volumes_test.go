package manager

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/pillow"
)

func TestPillowVolumesSurviveRestartAndAreRemovedWithBed(t *testing.T) {
	m, specs, _ := testPillowManager(t)
	specs = specs[:1]
	specs[0].Workdir = "/shared"
	specs[0].Mounts = []model.VolumeMount{{Volume: "shared", Path: "/shared"}}
	options := testPillowOptions(specs)
	options.Volumes = []model.VolumeSpec{{Name: "shared", Managed: &model.ManagedVolume{Persistence: "none"}}}
	options.Pillows[0].Mounts = []model.VolumeMount{{Volume: "shared", Path: "/shared"}}
	if _, err := m.InitializeBed(t.Context(), "volumes", options); err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(t.Context(), "volumes")
	if err != nil {
		t.Fatal(err)
	}
	before := waitPillow(t, m, b, "main", func(s pillow.Status) bool { return s.Ready })
	fs := b.filesystem
	if got, err := fs.Read("/shared/pillow-started"); err != nil || string(got) != "volumes" {
		t.Fatalf("pillow did not use its mount cwd: %q %v", got, err)
	}
	if err := fs.Write("/shared/preserved", []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := b.Spec().Dir
	if err := m.pillows.Restart(b.Bed, "main"); err != nil {
		t.Fatal(err)
	}
	waitPillow(t, m, b, "main", func(s pillow.Status) bool { return s.Ready && s.ExecutionID != before.ExecutionID })
	if got, err := fs.Read("/shared/preserved"); err != nil || string(got) != "preserved" {
		t.Fatalf("volume reset by pillow restart: %q %v", got, err)
	}
	realm := m.executorManager.Current(b.Bed)
	if err := realm.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	replacement, err := m.executorManager.For(t.Context(), b.Bed)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID() == realm.ID() {
		t.Fatal("Executor was not replaced")
	}
	if got, err := fs.Read("/shared/preserved"); err != nil || string(got) != "preserved" {
		t.Fatalf("Executor replacement lost volume: %q %v", got, err)
	}
	identity, err := os.ReadFile(m.identityPath("volumes"))
	if err != nil {
		t.Fatal(err)
	}
	var record localIdentityRecord
	if err := json.Unmarshal(identity, &record); err != nil {
		t.Fatal(err)
	}
	if len(record.Volumes) != 1 || len(record.Pillows[0].Mounts) != 1 {
		t.Fatalf("volume declaration not persisted: %+v", record)
	}
	if _, err := m.Evict(context.Background(), "volumes"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Bed data remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "data")); !os.IsNotExist(err) {
		t.Fatal("data remains")
	}
}

func TestVolumeDeclarationRecoveryPreservesLocalTemporaryData(t *testing.T) {
	root := t.TempDir()
	st := newFakeStore()
	m := storeTestManager(t, root, st)
	options := CreateOptions{Volumes: []model.VolumeSpec{{Name: "exchange", Managed: &model.ManagedVolume{Persistence: "none"}}}, Pillows: []model.PillowSpec{{Name: "main", Mounts: []model.VolumeMount{{Volume: "exchange", Path: "/exchange"}}}}}
	if _, err := m.InitializeBed(t.Context(), "recover", options); err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(t.Context(), "recover")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.filesystem.Write("/exchange/old-token", []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	next := storeTestManager(t, root, st)
	defer next.Close(t.Context())
	if _, err := next.InitializeBed(t.Context(), "recover", options); err != nil {
		t.Fatal(err)
	}
	restored, err := next.Ensure(t.Context(), "recover")
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Spec().Volumes) != 1 || len(restored.Spec().DefaultPillow().Mounts) != 1 {
		t.Fatal("local declaration lost")
	}
	if got, err := restored.filesystem.Read("/exchange/old-token"); err != nil || string(got) != "old" {
		t.Fatalf("local temporary volume lost: %v", err)
	}
	if _, err := next.InitializeBed(t.Context(), "recover", CreateOptions{Pillows: []model.PillowSpec{{Name: "main", Mounts: []model.VolumeMount{}}}}); !errors.Is(err, ErrPillowsConflict) {
		t.Fatalf("live mount mutation accepted: %v", err)
	}
}
