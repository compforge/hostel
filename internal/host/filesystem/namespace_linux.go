//go:build linux

package filesystem

import (
	"context"
	"debug/elf"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	CaptureArg      = "__mount_capture"
	BedInitMountArg = "__bedinit_mount"
	DeriveMountArg  = "__mount_derive"
)

// MountNamespace owns a prepared mount view and root, not an Executor process.
// Keeping the descriptors open pins the view after the preparation helper exits.
type MountNamespace struct {
	mount, root, uts, ipc, executable *os.File
}

// StaticBootstrap verifies that the trusted re-exec cannot load libraries from
// a workload-controlled rootfs before credentials have been dropped.
func StaticBootstrap() (string, error) {
	path := fmt.Sprintf("/proc/%d/exe", os.Getpid())
	f, err := elf.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	for _, program := range f.Progs {
		if program.Type == elf.PT_INTERP {
			return "", fmt.Errorf("rootful mount preparation requires a static Hostel binary (CGO_ENABLED=0)")
		}
	}
	return path, nil
}

// PrepareMountNamespace prepares resources without accepting a workload or its cwd/env.
// +spec=`Only a pinned static helper runs with preparation authority; handles are handed off after its capabilities are cleared`
// +link:doc=docs/isolation.md
func PrepareMountNamespace(ctx context.Context, bwrap, helper string, mounts []Mount) (*MountNamespace, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	executable, err := os.Open(helper)
	if err != nil {
		return nil, err
	}
	defer executable.Close()
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
	// The private root can belong to a different UID. Keep directory traversal
	// authority through bwrap's chdir/exec, and SETPCAP to clear the ceiling in
	// our fixed helper. No Pillow cwd, executable or environment is used here.
	argv := (Bubblewrap{UTSNamespace: true, IPCNamespace: true, Mounts: mounts, Cwd: "/", DieWithParent: true}).Args()
	argv = append(argv[:len(argv)-1], "--ro-bind", "/proc", "/proc", "--cap-drop", "ALL",
		"--cap-add", "CAP_DAC_OVERRIDE", "--cap-add", "CAP_SETPCAP", "--", "/proc/self/fd/3", CaptureArg)
	cmd := exec.CommandContext(ctx, bwrap, argv...)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.ExtraFiles = []*os.File{executable, send}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	send.Close()
	// SCM_RIGHTS transfers the prepared resources directly, without a PID-based
	// /proc lookup or keeping a privileged helper alive until the daemon opens them.
	view, err := receiveMountNamespace(conn.(*net.UnixConn), helper)
	if err != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	if err == nil {
		err = waitErr
	}
	if err != nil {
		if view != nil {
			_ = view.Close()
		}
		return nil, fmt.Errorf("prepare mount namespace: %w: %s", err, stderr.String())
	}
	return view, nil
}

func (n *MountNamespace) Close() error {
	var result error
	files := []*os.File{n.root, n.mount, n.uts, n.ipc, n.executable}
	n.root, n.mount, n.uts, n.ipc, n.executable = nil, nil, nil, nil, nil
	for _, file := range files {
		if file != nil {
			if err := file.Close(); err != nil && result == nil {
				result = err
			}
		}
	}
	return result
}

// Wrap enters already-prepared resources. Descriptor paths are owned by the
// daemon and stay live until Bed Manager has drained all executions and Services.
func (n *MountNamespace) Wrap(cmd *exec.Cmd) {
	inherited := func(file *os.File) string {
		path := fmt.Sprintf("/proc/self/fd/%d", 3+len(cmd.ExtraFiles))
		cmd.ExtraFiles = append(cmd.ExtraFiles, file)
		return path
	}
	executable := inherited(n.executable)
	namespaces := []string{inherited(n.mount), inherited(n.uts), inherited(n.ipc)}
	root := inherited(n.root)
	args := []string{executable, BedInitMountArg, strings.Join(namespaces, ","), root, "--", executable}
	cmd.Args = append(args, cmd.Args[1:]...)
	cmd.Path, cmd.Dir = executable, "/"
}

// RunMountHelper dispatches only trusted internal re-exec operations.
func RunMountHelper(args []string) error {
	if len(args) == 1 && args[0] == CaptureArg {
		runtime.LockOSThread()
		socket := os.NewFile(4, "mount-send")
		defer socket.Close()
		_ = unix.Close(3)
		return finishMountPreparation(int(socket.Fd()))
	}
	if len(args) < 5 || (args[0] != BedInitMountArg && args[0] != DeriveMountArg) || args[3] != "--" {
		return fmt.Errorf("invalid mount helper arguments")
	}
	if args[0] == DeriveMountArg {
		if len(args) != 5 {
			return fmt.Errorf("invalid derived mount arguments")
		}
		var socket int
		if _, err := fmt.Sscanf(args[4], "/proc/self/fd/%d", &socket); err != nil {
			return err
		}
		if err := enterMount(args[1], args[2]); err != nil {
			return err
		}
		// Chroot precedes unshare so the root is remapped into the copied tree.
		if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
			return err
		}
		if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
			return err
		}
		if err := unix.Mount("proc", "/proc", "proc", unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
			return fmt.Errorf("mount Executor procfs: %w", err)
		}
		// setns/unshare are thread-local. Send the actual handles from this locked
		// thread, rather than asking the daemon to inspect the thread-group leader.
		return finishMountPreparation(socket)
	}

	// Pin the next trusted executable before entering a workload-controlled root.
	next, err := os.Open(args[4])
	if err != nil {
		return err
	}
	defer next.Close()
	inherited := append(strings.Split(args[1], ","), args[2], args[4])
	for _, path := range inherited {
		var fd int
		if _, err := fmt.Sscanf(path, "/proc/self/fd/%d", &fd); err == nil {
			unix.CloseOnExec(fd)
		}
	}
	if err := enterMount(args[1], args[2]); err != nil {
		return err
	}
	return syscall.Exec(fmt.Sprintf("/proc/self/fd/%d", next.Fd()), args[4:], os.Environ())
}

// enterMount only enters a prepared view; it never creates namespace resources.
func enterMount(namespaces, rootPath string) error {
	paths := strings.Split(namespaces, ",")
	if len(paths) != 3 {
		return fmt.Errorf("incomplete prepared namespace handles")
	}
	var handles []*os.File
	defer func() {
		for _, handle := range handles {
			_ = handle.Close()
		}
	}()
	for _, path := range paths {
		handle, err := os.Open(path)
		if err != nil {
			return err
		}
		handles = append(handles, handle)
	}
	root, err := os.Open(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	// Namespace entry affects this thread; it must stay locked through exec/exit.
	runtime.LockOSThread()
	if err := unix.Unshare(unix.CLONE_FS); err != nil {
		return err
	}
	for i, kind := range []int{unix.CLONE_NEWNS, unix.CLONE_NEWUTS, unix.CLONE_NEWIPC} {
		if err := unix.Setns(int(handles[i].Fd()), kind); err != nil {
			return err
		}
	}
	if err := unix.Fchdir(int(root.Fd())); err != nil {
		return err
	}
	return unix.Chroot(".")
}
