//go:build linux

package manager

import (
	"os/exec"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/bed/privilege"
	hostprivilege "github.com/qiankunli/hostel/internal/host/privilege"
)

type testFileBoundary struct{ isolation.Stateless }

func (testFileBoundary) Name() string                        { return "test-file" }
func (testFileBoundary) Level() isolation.Level              { return isolation.Shared }
func (testFileBoundary) Available() bool                     { return true }
func (testFileBoundary) View(fs *bedfs.FS) bedfs.ProcessView { return bedfs.HostView(fs) }
func (testFileBoundary) MountsRoot() bool                    { return false }
func (testFileBoundary) Wrap(cmd *exec.Cmd, _ *bedfs.FS, _ string) error {
	cmd.Args = append([]string{"file-helper", "--", cmd.Path}, cmd.Args[1:]...)
	cmd.Path = "/file-helper"
	return nil
}

func TestEnvironmentDropsUserOutsideFileBoundary(t *testing.T) {
	helper, err := hostprivilege.ProcessCredentialHelper()
	if err != nil {
		t.Fatal(err)
	}
	fs, err := bedfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	environment := bindEnvironment(testFileBoundary{}, fs, nil, privilege.CurrentBedUser())
	cmd := exec.Command("/bin/echo", "ok")
	if err := environment.Wrap(cmd, ""); err != nil {
		t.Fatal(err)
	}
	if cmd.Path != helper {
		t.Fatalf("outer command = %q, want %q", cmd.Path, helper)
	}
	separator := -1
	for index, arg := range cmd.Args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(cmd.Args) || cmd.Args[separator+1] != "/file-helper" {
		t.Fatalf("wrapped args = %q", cmd.Args)
	}
}

func (testFileBoundary) AllowsMappings() bool { return true }
