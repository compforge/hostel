//go:build linux

package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// The supervisor is PID 1 for one Executor. Private procfs must be installed
// before any threads serve requests; all children inherit this mount view.
func initializePIDNamespace() (*os.File, error) {
	if os.Getpid() != 1 {
		return nil, fmt.Errorf("private PID namespace supervisor must be PID 1")
	}
	parentProc := os.NewFile(3, "parent-procfs")
	unix.CloseOnExec(3)
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		parentProc.Close()
		return nil, fmt.Errorf("make Executor mounts private: %w", err)
	}
	if err := unix.Mount("proc", "/proc", "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		parentProc.Close()
		return nil, fmt.Errorf("mount Executor procfs: %w", err)
	}
	return parentProc, nil
}

// parentPID resolves a live or unreaped child while the server's reaper is
// locked out. Namespace-local PIDs must never escape to daemon-side inspectors.
func parentPID(parentProc *os.File, pid int) (int, error) {
	if parentProc == nil {
		return pid, nil
	}
	root := fmt.Sprintf("/proc/self/fd/%d", parentProc.Fd())
	self, err := os.Readlink(filepath.Join(root, "self"))
	if err != nil {
		return 0, err
	}
	tasks, err := os.ReadDir(filepath.Join(root, self, "task"))
	if err != nil {
		return 0, err
	}
	for _, task := range tasks {
		children, err := os.ReadFile(filepath.Join(root, self, "task", task.Name(), "children"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		for _, child := range strings.Fields(string(children)) {
			status, err := os.ReadFile(filepath.Join(root, child, "status"))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return 0, err
			}
			for _, line := range strings.Split(string(status), "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 3 && fields[0] == "NSpid:" && fields[len(fields)-1] == strconv.Itoa(pid) {
					return strconv.Atoi(child)
				}
			}
		}
	}
	return 0, fmt.Errorf("parent PID unavailable for namespace child %d", pid)
}
