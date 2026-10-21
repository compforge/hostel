package filesystem

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
)

type mountedFiles struct{ carrierOnly }

func (mountedFiles) View(fs *bedfs.FS) bedfs.ProcessView {
	return bedfs.RootedView(fs, bedfs.MappingSupport{ReadWrite: true, ReadOnly: true})
}

func volumeBed(t *testing.T, name string) *bed.Bed {
	t.Helper()
	b := bed.New(name, "", bed.Spec{Dir: t.TempDir(), Volumes: []bed.VolumeSpec{{Name: "token", EmptyDir: &bed.EmptyDirVolume{}}}, Pillows: append([]bed.PillowSpec{{Name: "workspace", Mounts: []bed.VolumeMount{{Volume: "token", Path: "/run/bootstrap", ReadOnly: true}}}}, []bed.PillowSpec{{Name: "writer", Mounts: []bed.VolumeMount{{Volume: "token", Path: "/writer"}}}, {Name: "unmounted"}}...)})
	bed.NewOwners().Privilege.Set(b, bed.PrivilegeStatus{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()), Assigned: true})
	return b
}

// +case:id=volume_consumer_views,desc=`Pillow publishes a token into a shared volume`,expect=`ordinary file APIs can read but cannot alter it; an unmounted Pillow and another Bed cannot read it`
func TestVolumeConsumerViews(t *testing.T) {
	m := NewManager(mountedFiles{}, bed.NewOwners().Filesystem)
	b := volumeBed(t, "one")
	if err := m.Prepare(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Release(context.Background(), b); err != nil {
			t.Error(err)
		}
	})
	if err := m.PrepareView(t.Context(), b, nil); err != nil {
		t.Fatal(err)
	}
	main, writer := m.Files(b), m.PillowFiles(b)["writer"]
	if err := writer.Write("/writer/token.next", []byte("one"), 0444); err != nil {
		t.Fatal(err)
	}
	if err := writer.Rename("/writer/token.next", "/writer/token"); err != nil {
		t.Fatal(err)
	}
	if got, err := main.Read("/run/bootstrap/token"); err != nil || string(got) != "one" {
		t.Fatalf("read=%q err=%v", got, err)
	}
	mutations := []func() error{
		func() error { return main.Write("/run/bootstrap/token", []byte("bad"), 0600) },
		func() error { return main.Remove([]string{"/run/bootstrap/token"}) },
		func() error { return main.Rename("/run/bootstrap/token", "/run/bootstrap/replaced") },
		func() error { return main.Chmod("/run/bootstrap/token", bedfs.Permission{Mode: 0600}) },
		func() error {
			_, err := main.WriteTransferFile(t.Context(), "/run/bootstrap/token", strings.NewReader("bad"), true)
			return err
		},
	}
	for i, mutate := range mutations {
		if err := mutate(); !os.IsPermission(err) {
			t.Fatalf("mutation %d: %v", i, err)
		}
	}
	if err := main.RemoveDir("/run"); err == nil {
		t.Fatal("removed mount parent")
	}
	if _, err := m.PillowFiles(b)["unmounted"].Read("/run/bootstrap/token"); err == nil {
		t.Fatal("unmounted pillow inherited ordinary mounts")
	}
	other := volumeBed(t, "two")
	if err := m.Prepare(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	defer m.Release(t.Context(), other)
	if _, err := m.Files(other).Read("/run/bootstrap/token"); err == nil {
		t.Fatal("cross-Bed volume leak")
	}
	source, _ := writer.Resolve("/writer/token")
	if strings.HasPrefix(source, main.Rootfs()+string(filepath.Separator)) {
		t.Fatal("volume stored in snapshot root")
	}
	if err := writer.Remove([]string{"/writer/token"}); err != nil {
		t.Fatal(err)
	}
	if _, err := main.Read("/run/bootstrap/token"); !os.IsNotExist(err) {
		t.Fatalf("consumed token still visible: %v", err)
	}
	status := b.Status()
	status.Filesystem.Mounts[0].Path = "changed"
	if b.Status().Filesystem.Mounts[0].Path == "changed" {
		t.Fatal("status leaked mutable mount slice")
	}
}

func TestReadOnlyVolumeCannotSilentlyDegrade(t *testing.T) {
	m := NewManager(carrierOnly{}, bed.NewOwners().Filesystem)
	b := volumeBed(t, "unsupported")
	if err := m.Prepare(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	defer m.Release(t.Context(), b)
	if err := m.PrepareView(t.Context(), b, nil); err == nil || !strings.Contains(err.Error(), "read-only process mounts unavailable") {
		t.Fatalf("admission=%v", err)
	}
	if b.Status().Filesystem.Prepared {
		t.Fatal("unsupported view published")
	}
}

type partialVolumeViews struct {
	mountedFiles
	owned    map[*bedfs.FS]bool
	released map[*bedfs.FS]bool
}

func (p *partialVolumeViews) Prepare(_ context.Context, fs *bedfs.FS) error {
	p.owned[fs] = true
	if len(p.owned) == 2 {
		return errors.New("partial pillow view")
	}
	return nil
}
func (p *partialVolumeViews) Release(_ context.Context, fs *bedfs.FS) error {
	if p.owned[fs] {
		p.released[fs] = true
	}
	return nil
}
func TestPartialPillowViewReleasesEveryPreparedAllocation(t *testing.T) {
	iso := &partialVolumeViews{owned: map[*bedfs.FS]bool{}, released: map[*bedfs.FS]bool{}}
	m := NewManager(iso, bed.NewOwners().Filesystem)
	b := volumeBed(t, "partial")
	if err := m.Prepare(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if err := m.PrepareView(t.Context(), b, nil); err == nil {
		t.Fatal("expected preparation failure")
	}
	if err := m.Release(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if len(iso.owned) != 2 || len(iso.released) != 2 {
		t.Fatalf("prepared=%d released=%d", len(iso.owned), len(iso.released))
	}
	if err := m.Release(t.Context(), b); err != nil {
		t.Fatal(err)
	}
}

func TestHostVolumeViewsAndSourceOwnership(t *testing.T) {
	source := t.TempDir()
	b := bed.New("external", "", bed.Spec{
		Dir:     filepath.Join(t.TempDir(), "beds", "external"),
		Volumes: []bed.VolumeSpec{{Name: "external", HostPath: &bed.HostPathVolume{Path: source}}},

		Pillows: append([]bed.PillowSpec{{Name: "workspace", Mounts: []bed.VolumeMount{{Volume: "external", Path: "/input", ReadOnly: true}}}}, []bed.PillowSpec{{Name: "writer", Mounts: []bed.VolumeMount{{Volume: "external", Path: "/output"}}}, {Name: "unmounted"}}...),
	})
	m := NewManager(mountedFiles{}, bed.NewOwners().Filesystem)
	if err := m.Prepare(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	defer m.Release(t.Context(), b)
	if err := m.PrepareView(t.Context(), b, nil); err != nil {
		t.Fatal(err)
	}
	if m.volumes[b] != "" {
		t.Fatal("borrowed data allocated an empty_dir")
	}
	if err := m.PillowFiles(b)["writer"].Write("/output/value", []byte("shared"), 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := m.Files(b).Read("/input/value"); err != nil || string(data) != "shared" {
		t.Fatalf("shared read: %q %v", data, err)
	}
	if err := m.Files(b).Write("/input/value", []byte("bad"), 0600); !os.IsPermission(err) {
		t.Fatalf("read-only: %v", err)
	}
	if _, err := m.PillowFiles(b)["unmounted"].Read("/input/value"); !os.IsNotExist(err) {
		t.Fatalf("mount inherited: %v", err)
	}
	if err := m.Release(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(source, "value")); err != nil || string(data) != "shared" {
		t.Fatalf("external data lost: %q %v", data, err)
	}
}

func TestHostVolumeRejectsInvalidAndAliasedSources(t *testing.T) {
	root := t.TempDir()
	source := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(source, alias); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(source, "file")
	if err := os.WriteFile(file, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, paths := range [][]string{{filepath.Join(source, "missing")}, {file}, {root}, {source, alias}} {
		b := bed.New("invalid", "", bed.Spec{Dir: filepath.Join(root, "bed")})
		bed.SpecWriter{}.Update(b, func(s *bed.Spec) {
			for i, p := range paths {
				s.Volumes = append(s.Volumes, bed.VolumeSpec{Name: fmt.Sprintf("v%d", i), HostPath: &bed.HostPathVolume{Path: p}})
			}
		})
		m := NewManager(mountedFiles{}, bed.NewOwners().Filesystem)
		if err := m.Prepare(t.Context(), b); err == nil {
			t.Fatalf("invalid sources accepted: %v", paths)
		}
		if err := m.Release(t.Context(), b); err != nil {
			t.Fatal(err)
		}
	}
}
