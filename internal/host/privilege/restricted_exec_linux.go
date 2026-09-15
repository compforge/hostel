//go:build linux

package privilege

import (
	"fmt"
	"runtime"

	"golang.org/x/sys/unix"
)

// DropCredentials runs only in a disposable helper. It supports both managed
// identities and a rootless inherited identity without assuming host capabilities.
func DropCredentials(uid, gid int) error {
	runtime.LockOSThread()
	if uid < 0 || gid < 0 {
		return fmt.Errorf("privilege: invalid workload credentials or command")
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("privilege: no_new_privs: %w", err)
	}
	// Drop the ceiling while SETPCAP is still available. EINVAL marks the end
	// of the kernel's supported capability set, including on older kernels.
	var current [2]unix.CapUserData
	if err := unix.Capget(&unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}, &current[0]); err != nil {
		return err
	}
	canDropBounding := current[0].Effective&(1<<unix.CAP_SETPCAP) != 0
	for capability := 0; ; capability++ {
		present, err := unix.PrctlRetInt(unix.PR_CAPBSET_READ, uintptr(capability), 0, 0, 0)
		if err == unix.EINVAL {
			break
		}
		if err != nil {
			return fmt.Errorf("privilege: read capability ceiling: %w", err)
		}
		if !canDropBounding {
			if uid == 0 && present != 0 {
				return fmt.Errorf("privilege: inherited root requires an empty capability ceiling")
			}
			continue
		}
		if err := unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(capability), 0, 0, 0); err != nil {
			return fmt.Errorf("privilege: drop capability %d: %w", capability, err)
		}
	}
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
		return fmt.Errorf("privilege: clear ambient capabilities: %w", err)
	}
	// An unprivileged process cannot change groups. A privileged daemon must
	// clear them even when the chosen UID/GID happens to equal its own.
	if current[0].Effective&(1<<unix.CAP_SETGID) != 0 {
		if err := unix.Setgroups(nil); err != nil {
			return fmt.Errorf("privilege: clear supplementary groups: %w", err)
		}
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
	return nil
}
