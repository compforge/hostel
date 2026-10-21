//go:build linux

package filesystem

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	hostprivilege "github.com/qiankunli/hostel/internal/host/privilege"
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
	return receiveMountNamespace(conn.(*net.UnixConn), fmt.Sprintf("/proc/self/fd/%d", n.executable.Fd()))
}

func receiveMountNamespace(conn *net.UnixConn, helper string) (*MountNamespace, error) {
	rights := make([]byte, unix.CmsgSpace(4*4))
	_, size, flags, _, err := conn.ReadMsgUnix(make([]byte, 1), rights)
	if err != nil {
		return nil, fmt.Errorf("receive mount view: %w", err)
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
		return nil, fmt.Errorf("invalid mount handles: count=%d flags=%d", len(fds), flags)
	}
	executable, err := os.Open(helper)
	if err != nil {
		return nil, err
	}
	view := &MountNamespace{executable: executable, mount: os.NewFile(uintptr(fds[0]), "mount"),
		uts: os.NewFile(uintptr(fds[1]), "uts"), ipc: os.NewFile(uintptr(fds[2]), "ipc"), root: os.NewFile(uintptr(fds[3]), "root")}
	fds = nil // The returned view owns the transferred handles.
	return view, nil
}

// openMountNamespace captures this locked thread's actual view, including private roots.
func openMountNamespace() (*MountNamespace, error) {
	view := &MountNamespace{}
	for _, entry := range []struct {
		path string
		file **os.File
	}{
		{"/proc/thread-self/ns/mnt", &view.mount},
		{"/proc/thread-self/ns/uts", &view.uts},
		{"/proc/thread-self/ns/ipc", &view.ipc},
		{"/", &view.root},
	} {
		var err error
		*entry.file, err = os.Open(entry.path)
		if err != nil {
			view.Close()
			return nil, err
		}
	}
	return view, nil
}

func (n *MountNamespace) send(socket int) error {
	return unix.Sendmsg(socket, []byte{1}, unix.UnixRights(int(n.mount.Fd()), int(n.uts.Fd()), int(n.ipc.Fd()), int(n.root.Fd())), nil, 0)
}

// finishMountPreparation hands off resources only after the trusted preparer
// has relinquished its authority. Both template and derived views use this boundary.
func finishMountPreparation(socket int) error {
	view, err := openMountNamespace()
	if err != nil {
		return err
	}
	defer view.Close()
	if err := hostprivilege.DropCredentials(os.Getuid(), os.Getgid()); err != nil {
		return fmt.Errorf("finish mount preparation: %w", err)
	}
	return view.send(socket)
}
