//go:build linux

package manager

import (
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/bed/privilege"
	"os/exec"
	"testing"
)

type testFileBoundary struct{}

func (testFileBoundary) Name() string                 { return "test-file" }
func (testFileBoundary) Level() isolation.Level       { return isolation.Dorm }
func (testFileBoundary) Available() bool              { return true }
func (testFileBoundary) View(fs *bedfs.FS) bedfs.View { return bedfs.HostView(fs) }
func (testFileBoundary) WorkspaceMounted() bool       { return false }
func (testFileBoundary) Wrap(cmd *exec.Cmd, _ *bedfs.FS, _ string) error {
	cmd.Args = append([]string{"file-helper", "--", cmd.Path}, cmd.Args[1:]...)
	cmd.Path = "/file-helper"
	return nil
}

func TestEnvironmentDropsUserOutsideFileBoundary(t *testing.T) {
	setpriv, err := privilege.ProcessCredentialHelper()
	if err != nil {
		t.Skip("setpriv is not installed")
	}
	environment := bindEnvironment(testFileBoundary{}, nil, nil, privilege.CurrentBedUser())
	cmd := exec.Command("/bin/echo", "ok")
	if err := environment.Wrap(cmd, ""); err != nil {
		t.Fatal(err)
	}
	if cmd.Path != setpriv {
		t.Fatalf("outer command = %q, want %q", cmd.Path, setpriv)
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
