//go:build darwin

package executor

import (
	"errors"
	"os/exec"

	"golang.org/x/sys/unix"
)

// kqueue observes exit without reaping, preserving the child identity while
// a service drains its process group. os/exec remains the sole wait4 owner.
func waitCommandBeforeReap(cmd *exec.Cmd, markBeforeReap func(error) error) error {
	queue, err := unix.Kqueue()
	if err == nil {
		defer unix.Close(queue)
		event := unix.Kevent_t{Ident: uint64(cmd.Process.Pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ENABLE | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}
		var events [1]unix.Kevent_t
		for {
			_, err = unix.Kevent(queue, []unix.Kevent_t{event}, events[:], nil)
			if !errors.Is(err, unix.EINTR) {
				break
			}
		}
		if err == nil && events[0].Flags&unix.EV_ERROR != 0 && events[0].Data != 0 {
			err = unix.Errno(events[0].Data)
		}
		// A child that exited before registration is still ours and unreaped.
		if errors.Is(err, unix.ESRCH) {
			err = nil
		}
	}
	if markErr := markBeforeReap(err); markErr != nil {
		return errors.Join(err, markErr)
	}
	if err != nil {
		return errors.Join(err, cmd.Wait())
	}
	// Keep *exec.ExitError intact: it is a process outcome, not transport loss.
	return cmd.Wait()
}
