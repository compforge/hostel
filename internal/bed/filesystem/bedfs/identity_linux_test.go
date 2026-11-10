//go:build linux

package bedfs

import (
	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// +case:id=bedfs_identity_authority,desc=`Root daemon exposes private and borrowed data using one Bed identity`,expect=`new files belong to the Bed, shared group access works, and root-owned mutation and escaped symlinks fail`
func TestFileAPIUsesBedIdentity(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires Linux identity switching")
	}
	base, err := os.MkdirTemp("/tmp", "hostel-bedfs-identity-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	if err := os.Chmod(base, 0755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "beds", "one", "rootfs")
	source := filepath.Join(base, "software")
	for _, dir := range []string{home, source} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chown(home, 200001, 200001); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(source, 0, 199999); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(source, 0770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "root-owned"), []byte("root"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../../software/root-owned", filepath.Join(home, "outside")); err != nil {
		t.Fatal(err)
	}
	view, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	if err := view.SetMounts([]Mount{{Source: source, Target: "/software"}}); err != nil {
		t.Fatal(err)
	}
	worker, err := hostfs.NewIdentityRoots(200001, 200001, []int{199999})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	if err := view.BindIdentity(worker); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"/workspace/mine", "/software/installed"} {
		if err := view.Write(file, []byte("bed"), 0660); err != nil {
			t.Fatal(err)
		}
		full, _ := view.Resolve(file)
		info, err := os.Stat(full)
		if err != nil {
			t.Fatal(err)
		}
		if info.Sys().(*syscall.Stat_t).Uid != 200001 {
			t.Fatal("daemon owned API-created file")
		}
	}
	if err := view.Chmod("/software/root-owned", Permission{Mode: 0777}); !os.IsPermission(err) {
		t.Fatalf("root chmod authority leaked: %v", err)
	}
	if err := view.Write("/software/root-owned", []byte("changed"), 0644); !os.IsPermission(err) {
		t.Fatalf("root write authority leaked: %v", err)
	}
	if _, err := view.Read("/outside"); err == nil {
		t.Fatal("escaped symlink was readable")
	}
	if err := view.Chmod("/workspace/mine", Permission{Mode: 0600}); err != nil {
		t.Fatal(err)
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCommonParentDoesNotExposeBedData(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires a disposable root Linux runner")
	}
	base, err := os.MkdirTemp("/opt", "hostel-common-boundary-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	home := filepath.Join(base, "beds", "one", "rootfs")
	secret := filepath.Join(base, "beds", "two", "rootfs", "secret")
	for _, dir := range []string{home, filepath.Dir(secret)} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(secret, []byte("neighbor"), 0644); err != nil {
		t.Fatal(err)
	}
	view, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	if err := view.PrepareShared(); err != nil {
		t.Fatal(err)
	}
	// Both data trees are readable by this same identity. The common /opt route
	// still must not expose the neighboring tree through its Carrier spelling.
	if data, err := os.ReadFile(secret); err != nil || string(data) != "neighbor" {
		t.Fatalf("fixture %q %v", data, err)
	}
	if _, err := view.Read(secret); err == nil {
		t.Fatal("common parent exposed neighboring Bed")
	}
}
