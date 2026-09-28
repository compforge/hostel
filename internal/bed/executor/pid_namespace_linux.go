//go:build linux

package executor

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// Preserve the parent's procfs only inside the trusted supervisor so its IPC
// replies retain host PIDs for listener ownership and other daemon-side users.
func preparePIDNamespace(cmd *exec.Cmd) (func(), error) {
	parentProc, err := os.Open("/proc")
	if err != nil {
		return nil, fmt.Errorf("open parent procfs: %w", err)
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWPID | syscall.CLONE_NEWNS
	cmd.Args = append(cmd.Args, "--private-pid-namespace")
	cmd.ExtraFiles = append(cmd.ExtraFiles, parentProc)
	return func() { _ = parentProc.Close() }, nil
}
