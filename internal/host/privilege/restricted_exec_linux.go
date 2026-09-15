//go:build linux

package privilege

import (
	"fmt"
	"os"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

// ExecRestricted is the final transition from trusted resource preparation to
// workload code. Call only in a disposable, OS-thread-locked helper process.
// Neither the workload cwd nor its environment is used before dropping privilege.
func ExecRestricted(uid, gid int, cwd string, argv, env []string) error {
	runtime.LockOSThread()
	if uid < 0 || gid < 0 || len(argv) == 0 {
		return fmt.Errorf("privilege: invalid workload credentials or command")
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("privilege: no_new_privs: %w", err)
	}
	// Drop the ceiling while SETPCAP is still available. EINVAL marks the end
	// of the kernel's supported capability set, including on older kernels.
	for capability := 0; ; capability++ {
		_, err := unix.PrctlRetInt(unix.PR_CAPBSET_READ, uintptr(capability), 0, 0, 0)
		if err == unix.EINVAL {
			break
		}
		if err != nil {
			return fmt.Errorf("privilege: read capability ceiling: %w", err)
		}
		if err := unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(capability), 0, 0, 0); err != nil {
			return fmt.Errorf("privilege: drop capability %d: %w", capability, err)
		}
	}
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
		return fmt.Errorf("privilege: clear ambient capabilities: %w", err)
	}
	if err := unix.Setgroups(nil); err != nil {
		return fmt.Errorf("privilege: clear supplementary groups: %w", err)
	}
	if err := unix.Setresgid(gid, gid, gid); err != nil {
		return fmt.Errorf("privilege: set gid: %w", err)
	}
	if err := unix.Setresuid(uid, uid, uid); err != nil {
		return fmt.Errorf("privilege: set uid: %w", err)
	}
	var empty [2]unix.CapUserData
	if err := unix.Capset(&unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}, &empty[0]); err != nil {
		return fmt.Errorf("privilege: clear process capabilities: %w", err)
	}
	if err := os.Chdir(cwd); err != nil {
		return fmt.Errorf("privilege: workload cwd: %w", err)
	}
	return syscall.Exec(argv[0], argv, env)
}
