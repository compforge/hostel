//go:build linux

package privilege

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestChownTreeSkipsHardlinks(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to chown to a foreign uid and observe the skip")
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "host-owned")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(root, "captured")); err != nil {
		t.Fatalf("hardlink (same fs needed): %v", err)
	}
	normal := filepath.Join(root, "own")
	if err := os.WriteFile(normal, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	const uid = 200007
	if err := ChownTree(root, uid, uid); err != nil {
		t.Fatal(err)
	}
	ownerOf := func(path string) int {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return int(info.Sys().(*syscall.Stat_t).Uid)
	}
	if got := ownerOf(outside); got == uid {
		t.Fatalf("hardlinked host file was rehomed to target uid %d", uid)
	}
	if got := ownerOf(normal); got != uid {
		t.Fatalf("normal file owner = %d, want target uid %d", got, uid)
	}
}
