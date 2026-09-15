package filesystem

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
)

type carrierOnly struct{}

func (carrierOnly) Name() string                            { return "direct" }
func (carrierOnly) Level() isolation.Level                  { return isolation.Shared }
func (carrierOnly) Available() bool                         { return true }
func (carrierOnly) View(fs *bedfs.FS) bedfs.ProcessView     { return bedfs.HostView(fs) }
func (carrierOnly) WorkdirMounted() bool                    { return false }
func (carrierOnly) Wrap(*exec.Cmd, *bedfs.FS, string) error { return nil }

func TestAPIMappingPreparesWithoutProcessMappingAndPreservesSource(t *testing.T) {
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
	if err != nil {
		t.Fatalf("prepare=%v", err)
	}
	if !b.Status().Filesystem.Prepared {
		t.Fatal("API mapping was not prepared")
	}
	fs := manager.Files(b)
	got, readErr := bedfs.NewReader(carrierOnly{}.View(fs), "").Read("/project/data.txt")
	if readErr != nil || string(got) != "external" {
		t.Fatalf("API mapping read=%q %v", got, readErr)
	}
	if err := manager.Release(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "external" {
		t.Fatalf("external source changed: %q %v", got, err)
	}
}

func (carrierOnly) AllowsMappings() bool { return true }
