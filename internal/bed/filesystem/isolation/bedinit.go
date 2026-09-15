package isolation

import (
	"os/exec"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	hostprivilege "github.com/qiankunli/hostel/internal/host/privilege"
	hostprocess "github.com/qiankunli/hostel/internal/host/process"
)

// BindBedInit binds bedinit to the resident Bed's execution plan.
// bedinit enters the environment, drops credentials and execs the workload. It
// never probes, allocates or selects a backend; those belong to Bed Manager.
func BindBedInit(files Isolator, fs *bedfs.FS, uid, gid int) func(*exec.Cmd, string) error {
	helper := hostprocess.Executable()
	if files.View(fs).Rootfs() == "/" {
		helper = hostprocess.BedInitPath
	}
	enter := files.Wrap
	if files.PrivilegedEntry() {
		enter = func(cmd *exec.Cmd, fs *bedfs.FS, cwd string) error {
			// Namespace entry pins this host executable before entering the Bed.
			// Never execute a Bed-controlled pathname while still privileged.
			hostprivilege.WrapBedInit(cmd, hostprocess.Executable(), uid, gid)
			return files.Wrap(cmd, fs, cwd)
		}
	} else {
		enter = func(cmd *exec.Cmd, fs *bedfs.FS, cwd string) error {
			if err := files.Wrap(cmd, fs, cwd); err != nil {
				return err
			}
			hostprivilege.WrapBedInit(cmd, hostprocess.Executable(), uid, gid)
			return nil
		}
	}
	return func(cmd *exec.Cmd, cwd string) error {
		processCwd, err := files.View(fs).Path(commandCwd(fs, cwd))
		if err != nil {
			return err
		}
		hostprocess.WrapBedInit(cmd, helper, processCwd)
		return enter(cmd, fs, cwd)
	}
}
