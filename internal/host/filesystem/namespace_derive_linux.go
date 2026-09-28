//go:build linux

package filesystem

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

// Derive instantiates a retained template inside the caller's process realm.
// start keeps the helper supervised; its owner waits/reaps it after return.
func (n *MountNamespace) Derive(ctx context.Context, start func(*exec.Cmd) error) (*MountNamespace, error) {
	sockets, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	receive := os.NewFile(uintptr(sockets[0]), "mount-receive")
	send := os.NewFile(uintptr(sockets[1]), "mount-send")
	defer send.Close()
	conn, err := net.FileConn(receive)
	receive.Close()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	cmd := &exec.Cmd{Args: []string{"hostel"}, Env: []string{"PATH=/usr/bin:/bin"}, Stderr: os.Stderr}
	n.Wrap(cmd)
	cmd.Args[1] = DeriveMountArg
	cmd.Args[5] = fmt.Sprintf("/proc/self/fd/%d", 3+len(cmd.ExtraFiles))
	cmd.ExtraFiles = append(cmd.ExtraFiles, send)
	if err := start(cmd); err != nil {
		return nil, err
	}
	send.Close()
	rights := make([]byte, unix.CmsgSpace(4*4))
	_, size, flags, _, err := conn.(*net.UnixConn).ReadMsgUnix(make([]byte, 1), rights)
	if err != nil {
		return nil, fmt.Errorf("receive Executor mount view: %w", err)
	}
	messages, err := unix.ParseSocketControlMessage(rights[:size])
	if err != nil {
		return nil, err
	}
	var fds []int
	defer func() {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}()
	for _, message := range messages {
		received, err := unix.ParseUnixRights(&message)
		if err != nil {
			return nil, err
		}
		for _, fd := range received {
			unix.CloseOnExec(fd)
		}
		fds = append(fds, received...)
	}
	if flags&unix.MSG_CTRUNC != 0 || len(fds) != 4 {
		return nil, fmt.Errorf("invalid Executor mount handles: count=%d flags=%d", len(fds), flags)
	}
	executable, err := os.Open(fmt.Sprintf("/proc/self/fd/%d", n.executable.Fd()))
	if err != nil {
		return nil, err
	}
	view := &MountNamespace{executable: executable, mount: os.NewFile(uintptr(fds[0]), "mount"),
		uts: os.NewFile(uintptr(fds[1]), "uts"), ipc: os.NewFile(uintptr(fds[2]), "ipc"), root: os.NewFile(uintptr(fds[3]), "root")}
	fds = nil // The returned view owns the transferred handles.
	return view, nil
}

func sendMountHandles(socket int) error {
	var handles []*os.File
	defer func() {
		for _, handle := range handles {
			_ = handle.Close()
		}
	}()
	var fds []int
	for _, path := range []string{"/proc/thread-self/ns/mnt", "/proc/thread-self/ns/uts", "/proc/thread-self/ns/ipc", "/"} {
		handle, err := os.Open(path)
		if err != nil {
			return err
		}
		handles = append(handles, handle)
		fds = append(fds, int(handle.Fd()))
	}
	return unix.Sendmsg(socket, []byte{1}, unix.UnixRights(fds...), nil, 0)
}
