package cgroup

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func testRoot(t *testing.T) *Group {
	t.Helper()
	root, err := openGroup(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	return root
}
func TestOldGroupCannotCloseReplacement(t *testing.T) {
	root := testRoot(t)
	old, err := root.CreateChild("worker")
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	fresh, err := root.CreateChild("worker")
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Open(); err == nil {
		t.Fatal("closed handle admitted process placement")
	}
	fd, err := fresh.Open()
	if err != nil {
		t.Fatalf("old cleanup removed replacement: %v", err)
	}
	fd.Close()
}
func TestFailedCleanupRetainsAllocation(t *testing.T) {
	root := testRoot(t)
	child, err := root.CreateChild("worker")
	if err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(child.path, "unexpected-file")
	if err := os.WriteFile(blocker, []byte("busy"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := child.Close(); err == nil {
		t.Fatal("failed teardown reported success")
	}
	if _, err := child.Open(); err == nil {
		t.Fatal("retiring allocation admitted a process")
	}
	if _, err := root.CreateChild("worker"); err == nil {
		t.Fatal("failed teardown permitted name reuse")
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := child.Close(); err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
	if _, err := root.CreateChild("worker"); err != nil {
		t.Fatal(err)
	}
}
func TestBorrowedRootPreservesUnownedGroups(t *testing.T) {
	root := testRoot(t)
	foreign := filepath.Join(root.path, "other-owner")
	if err := os.Mkdir(foreign, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := root.CreateChild("owned"); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("unowned group removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root.path, "owned")); !os.IsNotExist(err) {
		t.Fatalf("owned group not removed: %v", err)
	}
}
func TestGroupBookkeepingDoesNotRetainRetiredChildren(t *testing.T) {
	root := testRoot(t)
	for i := 0; i < 100; i++ {
		child, err := root.CreateChild(fmt.Sprintf("worker-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if err := child.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if len(root.children) > 1 {
		t.Fatalf("retained %d dead children", len(root.children))
	}
}
func TestChildNameCannotEscapeHierarchy(t *testing.T) {
	root := testRoot(t)
	for _, name := range []string{"", ".", "..", "../outside", "/absolute", "nested/child"} {
		if _, err := root.OpenChild(name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
}
