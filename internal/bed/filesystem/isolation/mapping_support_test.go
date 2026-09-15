package isolation

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
)

func TestHelpersOnlyRedirectSupportedMappings(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "beds", "one", "data")
	if err := os.MkdirAll(filepath.Join(home, "workspace"), 0755); err != nil {
		t.Fatal(err)
	}
	fs, err := bedfs.New(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	rw, ro := filepath.Join(base, "rw"), filepath.Join(base, "ro")
	for _, source := range []string{rw, ro} {
		if err := os.Mkdir(source, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := fs.SetPathMappings([]model.PathMapping{
		{HostPath: rw, BedPath: "/project"},
		{HostPath: ro, BedPath: "/reference", ReadOnly: true},
	}); err != nil {
		t.Fatal(err)
	}
	for _, blocked := range []bool{false, true} {
		for _, backend := range []processViewBackend{
			&prootView{path: "/usr/bin/proot", mappingsBlocked: blocked},
			&pathshimView{path: "/usr/bin/pathshim", mappingsBlocked: blocked},
		} {
			mappings := processMappings(fs, backend.MappingSupport())
			targets := make([]string, 0, len(mappings))
			for _, m := range mappings {
				targets = append(targets, m.Target)
			}
			if !slices.Contains(targets, bedfs.DefaultWorkdir) || slices.Contains(targets, "/reference") || slices.Contains(targets, "/project") == blocked {
				t.Fatalf("%s blocked=%t targets=%v", backend.Mode(), blocked, targets)
			}
			cwd, err := fs.Resolve("/project")
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("/bin/sh", "-c", "pwd")
			if err := backend.Wrap(cmd, fs, cwd); err != nil {
				t.Fatal(err)
			}
			expected := "/project"
			if blocked {
				expected = cwd
			}
			if got, err := backend.View(fs).Path(cwd); err != nil || got != expected {
				t.Fatalf("view=%q %v want=%q", got, err, expected)
			}
		}
	}
}
