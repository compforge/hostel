//go:build linux

package filesystem

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/tool"
)

func TestSharedSoftwareIsPreparedOnceAndRetainedOnClose(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires authority to assign shared software group")
	}
	root := t.TempDir()
	m := NewManager(carrierOnly{}, bed.NewOwners().Filesystem)
	m.softwareGroupAvailable = true
	m.sharedSoftware = []sharedDirectory{{Path: root, GID: 1000, Policy: tool.Required}}
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if s := m.Status().SharedSoftware; len(s) != 1 || !s[0].Tool.Selected {
		t.Fatalf("preparation not published: %+v", s)
	}
	file := filepath.Join(root, "explicit-mode")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("Start repeated the permission walk")
	}
	if raw, err := os.ReadFile(file); err != nil || string(raw) != "keep" {
		t.Fatal("Close removed Carrier software")
	}
}
