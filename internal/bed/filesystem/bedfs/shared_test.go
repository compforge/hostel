package bedfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSharedSoftwareFileView(t *testing.T) {
	home := t.TempDir()
	source := t.TempDir()
	view, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	shared, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	shared.paths.prefix = SoftwareRoot
	shared.software = true
	view.shared = []*FS{shared}
	if err := view.Write("/usr/local/tool", []byte("common"), 0644); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(source, "tool")); err != nil || string(got) != "common" {
		t.Fatalf("common file %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(home, "usr/local/tool")); !os.IsNotExist(err) {
		t.Fatalf("duplicated common software: %v", err)
	}
	if got, err := RootedView(view, MappingSupport{ReadWrite: true}).Path(filepath.Join(source, "tool")); err != nil || got != "/usr/local/tool" {
		t.Fatalf("process projection %q %v", got, err)
	}
	if err := view.RemoveDir(SoftwareRoot); err == nil {
		t.Fatal("removed common root")
	}
	shared.readOnly = true
	if err := view.Write("/usr/local/tool", nil, 0); !os.IsPermission(err) {
		t.Fatalf("controlled path allowed write: %v", err)
	}
}
