//go:build linux

package filesystem

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// +case:id=carrier_shared_software,desc=`Runtime directory preparation`,expect=`Group write and ACL inheritance work without changing UIDs or external symlink targets; hardlinks are rejected`
func TestPrepareSharedSoftwareRuntime(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "existing"), []byte("software"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "external-link")); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareSharedSoftware(t.Context(), alias, os.Getgid()); err == nil {
		t.Fatal("accepted symlinked software root")
	}
	gid := os.Getgid()
	for i := 0; i < 2; i++ {
		if _, err := PrepareSharedSoftware(t.Context(), root, gid); err != nil {
			t.Fatal(err)
		}
	}
	old, err := os.Stat(filepath.Join(root, "existing"))
	if err != nil {
		t.Fatal(err)
	}
	if old.Mode().Perm()&0060 != 0060 || int(old.Sys().(*syscall.Stat_t).Uid) != os.Getuid() {
		t.Fatal("group access or UID ownership changed incorrectly")
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "new"), []byte("new"), 0666); err != nil {
		t.Fatal(err)
	}
	f, err := os.Stat(filepath.Join(root, "nested", "new"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Mode().Perm()&0060 != 0060 || int(f.Sys().(*syscall.Stat_t).Gid) != gid {
		t.Fatalf("inheritance failed: %v", f)
	}
	external, _ := os.Stat(outside)
	if external.Mode().Perm() != 0600 {
		t.Fatal("followed external symlink")
	}
	if err := os.Link(outside, filepath.Join(root, "hardlink")); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareSharedSoftware(t.Context(), root, gid); err == nil {
		t.Fatal("accepted multiply-linked inode")
	}
	external, _ = os.Stat(outside)
	if external.Mode().Perm() != 0600 {
		t.Fatal("mutated outside hardlink")
	}
}

func TestSharedSoftwareRequiresActualGroupMembership(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires changing child process credentials")
	}
	root, err := os.MkdirTemp("", "hostel-shared-software-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareSharedSoftware(t.Context(), root, 1000); err != nil {
		t.Fatal(err)
	}
	run := func(uid uint32, groups []uint32, script string) error {
		cmd := exec.Command("/bin/sh", "-ec", script, "sh", root)
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid, Groups: groups}}
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("uid=%d groups=%v: %v %s", uid, groups, err, output)
		}
		return err
	}
	if err := run(200001, []uint32{1000}, `umask 022; mkdir "$1/nested"; printf first > "$1/nested/package"`); err != nil {
		t.Fatal(err)
	}
	if err := run(200002, []uint32{1000}, `printf second >> "$1/nested/package"`); err != nil {
		t.Fatal(err)
	}
	if err := run(200003, []uint32{}, `printf forbidden >> "$1/nested/package"`); err == nil {
		t.Fatal("UID without shared group wrote software")
	}
}

func TestSharedSoftwarePreservesNamedDefaultACL(t *testing.T) {
	root := t.TempDir()
	acl, err := sharedDefaultACL(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Add a named user read grant in canonical POSIX ACL order.
	named := make([]byte, len(acl)+8)
	copy(named, acl[:12])
	binary.LittleEndian.PutUint16(named[12:], 2)
	binary.LittleEndian.PutUint16(named[14:], 4)
	binary.LittleEndian.PutUint32(named[16:], 12345)
	copy(named[20:], acl[12:])
	if err := unix.Setxattr(root, "system.posix_acl_default", named, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareSharedSoftware(t.Context(), root, os.Getgid()); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(named))
	n, err := unix.Getxattr(root, "system.posix_acl_default", got)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(named) || binary.LittleEndian.Uint16(got[12:]) != 2 || binary.LittleEndian.Uint16(got[14:]) != 4 || binary.LittleEndian.Uint32(got[16:]) != 12345 {
		t.Fatal("existing named ACL grant was erased")
	}
}

// +case:id=shared_software_access_acl,desc=`Existing software has an extended access ACL with a read-only owning group`,expect=`Preparation grants the actual shared group write without erasing named grants`
func TestSharedSoftwareUpdatesExtendedAccessACL(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires changing child credentials")
	}
	root, err := os.MkdirTemp("", "hostel-shared-access-acl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(root, "package")
	if err := os.WriteFile(name, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	acl, err := sharedDefaultACL(nil)
	if err != nil {
		t.Fatal(err)
	}
	named := make([]byte, len(acl)+8)
	copy(named, acl[:12])
	binary.LittleEndian.PutUint16(named[12:], 2)
	binary.LittleEndian.PutUint16(named[14:], 4)
	binary.LittleEndian.PutUint32(named[16:], 12345)
	copy(named[20:], acl[12:])
	binary.LittleEndian.PutUint16(named[22:], 4) // owning group is read-only
	if err := unix.Setxattr(name, "system.posix_acl_access", named, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareSharedSoftware(t.Context(), root, 1000); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-ec", `printf updated >> "$1"`, "sh", name)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 200001, Gid: 200001, Groups: []uint32{1000}}}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shared group write: %v %s", err, output)
	}
	got := make([]byte, len(named))
	if _, err := unix.Getxattr(name, "system.posix_acl_access", got); err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(got[16:]) != 12345 || binary.LittleEndian.Uint16(got[14:]) != 4 {
		t.Fatal("named access grant changed")
	}
}
