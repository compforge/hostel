package filesystem

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
)

type carrierOnly struct{}

func (carrierOnly) Name() string                            { return "direct" }
func (carrierOnly) Level() isolation.Level                  { return isolation.Shared }
func (carrierOnly) Available() bool                         { return true }
func (carrierOnly) View(fs *bedfs.FS) bedfs.View            { return bedfs.HostView(fs) }
func (carrierOnly) WorkspaceMounted() bool                  { return false }
func (carrierOnly) Wrap(*exec.Cmd, *bedfs.FS, string) error { return nil }

func TestUnsupportedMappingFailsPreparationWithoutOwningSource(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "volume")
	if err := os.Mkdir(source, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(source, "data.txt")
	if err := os.WriteFile(file, []byte("external"), 0644); err != nil {
		t.Fatal(err)
	}
	b := bed.New("one", "", bed.Spec{Dir: filepath.Join(base, "beds", "one"), PathMappings: []bed.PathMapping{{HostPath: source, BedPath: "/project"}}})
	manager := NewManager(carrierOnly{}, bed.NewOwners().Filesystem)
	err := manager.Prepare(context.Background(), b)
	if err == nil || !strings.Contains(err.Error(), "require a process path view") {
		t.Fatalf("prepare=%v", err)
	}
	if b.Status().Filesystem.Prepared {
		t.Fatal("unsupported mapping published as prepared")
	}
	if err := manager.Release(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "external" {
		t.Fatalf("external source changed: %q %v", got, err)
	}
}
