package filesystem

import (
	"context"
	"errors"
	"testing"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
)

type releaseFailure struct {
	carrierOnly
	fail bool
}

func (f *releaseFailure) Release(context.Context, *bedfs.FS) error {
	if f.fail {
		return errors.New("partial cleanup")
	}
	return nil
}

// +case:id=fs_realization_lifecycle,desc=`Release fails after a Bed file view was prepared`,expect=`active claims are cleared, the owner survives, and retry reports released`
func TestRealizationFollowsPreparationAndCleanup(t *testing.T) {
	iso := &releaseFailure{fail: true}
	owners := bed.NewOwners()
	m := NewManager(iso, owners.Filesystem)
	b := bed.New("files", "", bed.Spec{Dir: t.TempDir()})
	if err := m.Prepare(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if got := b.Status().Filesystem.Realization; got == nil || got.State != "pending" {
		t.Fatalf("before prepare: %+v", got)
	}
	setFileTestIdentity(b)
	if err := m.PrepareView(t.Context(), b, nil); err != nil {
		t.Fatal(err)
	}
	got := b.Status().Filesystem.Realization
	if got.State != "prepared" || got.PrivateData != "none" || got.CoreAccess != "unix_permissions" || got.FileAPI != "bed_identity" || len(got.Views) != 1 || got.Views[0].Mode != "carrier" {
		t.Fatalf("prepared: %+v", got)
	}
	// Both the writer and a caller must receive detached observations.
	got.Views[0].Mode = "mutated"
	owners.Filesystem.Update(b, func(s *bed.FilesystemStatus) { got = s.Realization })
	got.Views[0].Mode = "mutated"
	if b.Status().Filesystem.Realization.Views[0].Mode != "carrier" {
		t.Fatal("published status aliases caller")
	}
	fs := m.Files(b)
	if err := m.Release(t.Context(), b); err == nil {
		t.Fatal("cleanup failure lost")
	}
	failed := b.Status().Filesystem
	if failed.Prepared || failed.Room != "" || failed.Realization.State != "unknown" || failed.Realization.FileAPI != "" || len(failed.Realization.Views) != 0 || m.Files(b) != fs {
		t.Fatalf("stale claims or lost owner: %+v", failed)
	}
	iso.fail = false
	if err := m.Release(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if m.Files(b) != nil || b.Status().Filesystem.Realization.State != "released" {
		t.Fatal("release not observed")
	}
}

func TestIndependentIdentityProtectsDataWithoutPathMechanism(t *testing.T) {
	m := NewManager(carrierOnly{}, bed.NewOwners().Filesystem)
	m.SetDedicatedIdentity(true)
	b := bed.New("identity", "", bed.Spec{})
	got := m.preparedRealization(b)
	if got.PrivateData != "uid" || got.CoreAccess != "unix_permissions" {
		t.Fatalf("identity conflated with path view: %+v", got)
	}
}

type preparationFailure struct{ carrierOnly }

func (preparationFailure) Prepare(context.Context, *bedfs.FS) error {
	return errors.New("view preparation failed")
}

// +case:id=fs_realization_prepare_failure,desc=`A selected file mechanism fails during preparation`,expect=`no prepared guarantee is published and cleanup retains its owner`
func TestFailedViewDoesNotPublishRealization(t *testing.T) {
	m := NewManager(preparationFailure{}, bed.NewOwners().Filesystem)
	b := bed.New("failed", "", bed.Spec{Dir: t.TempDir()})
	t.Cleanup(func() {
		if err := m.Release(t.Context(), b); err != nil {
			t.Error(err)
		}
	})
	if err := m.Prepare(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	setFileTestIdentity(b)
	if err := m.PrepareView(t.Context(), b, nil); err == nil {
		t.Fatal("preparation failure lost")
	}
	got := b.Status().Filesystem
	if got.Prepared || got.Realization.State != "pending" || got.Realization.PrivateData != "" || got.Realization.FileAPI != "" || m.Files(b) == nil {
		t.Fatalf("failed preparation invented guarantees or lost owner: %+v", got)
	}
}
