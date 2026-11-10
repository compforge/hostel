//go:build linux

package filesystem

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// +case:id=file_api_user_authority,desc=`A root parent requests file operations through a Bed-identity worker`,expect=`shared group writes work, neighbor/root-private reads and chmod fail, and an escaped symlink is refused`
func TestUserFilesUseKernelIdentity(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires identity switching")
	}
	base, err := os.MkdirTemp("/tmp", "hostel-file-identity-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	if err := os.Chmod(base, 0755); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name     string
		uid, gid int
		mode     os.FileMode
	}{{"own", 200001, 200001, 0700}, {"neighbor", 200002, 200002, 0700}, {"control", 0, 0, 0700}, {"shared", 0, 199999, 0770}} {
		name := filepath.Join(base, item.name)
		if err := os.Mkdir(name, item.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(name, item.uid, item.gid); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(name, item.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(name, "value"), []byte(item.name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../shared/value", filepath.Join(base, "own", "alias")); err != nil {
		t.Fatal(err)
	}
	worker, err := NewUserFiles(200001, 200001, []int{199999})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	result, file, err := worker.call(fileRequest{Operation: "new_root", Path: base})
	if file != nil {
		file.Close()
		t.Fatal("root creation returned file")
	}
	if err != nil {
		t.Fatal(err)
	}
	id := result.Root
	for _, name := range []string{"neighbor/value", "control/value"} {
		_, f, err := worker.call(fileRequest{Operation: "open", Root: id, Path: name})
		if f != nil {
			f.Close()
		}
		if !os.IsPermission(err) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	_, f, err := worker.call(fileRequest{Operation: "open", Root: id, Path: "shared/new", Flags: os.O_CREATE | os.O_WRONLY, Mode: 0660})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("shared"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	info, err := os.Stat(filepath.Join(base, "shared/new"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Sys().(*syscall.Stat_t).Uid != 200001 {
		t.Fatal("file was created by privileged parent")
	}
	_, f, err = worker.call(fileRequest{Operation: "open", Root: id, Path: "shared/value"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(f)
	f.Close()
	if err != nil || string(raw) != "shared" {
		t.Fatalf("read %q %v", raw, err)
	}
	_, _, err = worker.call(fileRequest{Operation: "chmod", Root: id, Path: "shared/value", Mode: 0777})
	if !os.IsPermission(err) {
		t.Fatalf("borrowed daemon chmod authority: %v", err)
	}
	own, _, err := worker.call(fileRequest{Operation: "open_root", Root: id, Path: "own"})
	if err != nil {
		t.Fatal(err)
	}
	_, f, err = worker.call(fileRequest{Operation: "open", Root: own.Root, Path: "alias"})
	if f != nil {
		f.Close()
	}
	if err == nil {
		t.Fatal("symlink escaped source")
	}
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := worker.call(fileRequest{Operation: "ping"}); err == nil {
		t.Fatal("closed worker accepted operation")
	}
}
