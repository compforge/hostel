package bedfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// +case:id=common_view_preserves_source,desc=`Carrier software and dependencies enter a Bed view`,expect=`mapping reuses the source without changing existing modes, and read-only routes reject writes`
func TestCommonFileViewPreservesSource(t *testing.T) {
	for _, target := range []string{"/usr/local", "/opt/runtime", "/srv/shared-data"} {
		t.Run(target, func(t *testing.T) {
			home, source := t.TempDir(), t.TempDir()
			existing := filepath.Join(source, "existing")
			if err := os.WriteFile(existing, []byte("provided"), 0640); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(existing)
			if err != nil {
				t.Fatal(err)
			}
			view, err := New(home)
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			shared, err := New(source)
			if err != nil {
				t.Fatal(err)
			}
			shared.paths.prefix = target
			shared.common = true
			view.shared = []*FS{shared}
			if got, err := view.Read(target + "/existing"); err != nil || string(got) != "provided" {
				t.Fatalf("provided file %q %v", got, err)
			}
			if err := view.Write(target+"/tool", []byte("common"), 0644); err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(filepath.Join(source, "tool")); err != nil || string(got) != "common" {
				t.Fatalf("common file %q %v", got, err)
			}
			// Creation follows the public-source policy at any logical path.
			if _, err := view.WriteTransferFile(t.Context(), target+"/download", strings.NewReader("shared"), false); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(filepath.Join(source, "download"))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0660 {
				t.Fatalf("public transfer mode=%o, want 660", info.Mode().Perm())
			}
			if _, err := view.WriteTransferFile(t.Context(), "/private", strings.NewReader("private"), false); err != nil {
				t.Fatal(err)
			}
			info, err = os.Stat(filepath.Join(home, "private"))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0644 {
				t.Fatalf("private transfer inherited public mode: %o", info.Mode().Perm())
			}
			if _, err := os.Stat(filepath.Join(home, target, "tool")); !os.IsNotExist(err) {
				t.Fatalf("duplicated common source: %v", err)
			}
			if got, err := RootedView(view, MappingSupport{ReadWrite: true}).Path(filepath.Join(source, "tool")); err != nil || got != target+"/tool" {
				t.Fatalf("process projection %q %v", got, err)
			}
			if err := view.RemoveDir(target); err == nil {
				t.Fatal("removed common root")
			}
			shared.readOnly = true
			if err := view.Write(target+"/existing", []byte("changed"), 0); !os.IsPermission(err) {
				t.Fatalf("controlled path allowed write: %v", err)
			}
			if err := view.Close(); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(existing)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
				t.Fatal("mapping modified the provided file")
			}
		})
	}
}
