//go:build linux

package filesystem

import (
	"context"
	"debug/elf"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	hostprivilege "github.com/qiankunli/hostel/internal/host/privilege"
	"golang.org/x/sys/unix"
)

const (
	CaptureArg        = "__mount_capture"
	EnterArg          = "__mount_enter"
	workloadEnvPrefix = "HOSTEL_WORKLOAD_ENV_"
)

// MountNamespace owns a prepared mount view and root, not an Executor process.
// Keeping the descriptors open pins the view after the preparation helper exits.
type MountNamespace struct {
	mount, root, uts, ipc *os.File
	helper                string
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

// PrepareMountNamespace runs bubblewrap with host privileges only during
// preparation. The sole program it executes is our pinned static helper.
// No workload arguments, environment or code participate in this stage.
func PrepareMountNamespace(ctx context.Context, bwrap, helper string, args []string) (*MountNamespace, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	executable, err := os.Open(helper)
	if err != nil {
		return nil, err
	}
	defer executable.Close()
	ready, notify, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer ready.Close()
	defer notify.Close()
	wait, release, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer wait.Close()
	defer release.Close()
	// /proc is a reserved runtime overlay: it resolves the inherited executable
	// FD without executing a program supplied by the workload rootfs.
	argv := append([]string(nil), args[:len(args)-1]...)
	argv = append(argv, "--ro-bind", "/proc", "/proc", "--cap-drop", "ALL", "--",
		"/proc/self/fd/3", CaptureArg)
	cmd := exec.CommandContext(ctx, bwrap, argv...)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.ExtraFiles = []*os.File{executable, notify, wait}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	notify.Close()
	wait.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	// The helper reports readiness only after bwrap has installed the rootfs.
	// A deadline also bounds a helper/tool that never reaches that point.
	_ = ready.SetReadDeadline(time.Now().Add(10 * time.Second))
	var pid int
	err = json.NewDecoder(ready).Decode(&pid)
	var ns *MountNamespace
	if err == nil {
		ns = &MountNamespace{helper: helper}
		ns.mount, err = os.Open(fmt.Sprintf("/proc/%d/ns/mnt", pid))
		if err == nil {
			ns.root, err = os.Open(fmt.Sprintf("/proc/%d/root", pid))
		}
		if err == nil {
			ns.uts, err = os.Open(fmt.Sprintf("/proc/%d/ns/uts", pid))
		}
		if err == nil {
			ns.ipc, err = os.Open(fmt.Sprintf("/proc/%d/ns/ipc", pid))
		}
	}
	release.Close()
	select {
	case waitErr := <-done:
		if err == nil {
			err = waitErr
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		if err == nil {
			err = fmt.Errorf("mount preparation helper did not exit")
		}
	}
	if err != nil {
		if ns != nil {
			_ = ns.Close()
		}
		return nil, fmt.Errorf("prepare mount namespace: %w: %s", err, stderr.String())
	}
	return ns, nil
}

func (n *MountNamespace) Close() error {
	var result error
	files := []*os.File{n.root, n.mount, n.uts, n.ipc}
	n.root, n.mount, n.uts, n.ipc = nil, nil, nil, nil
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
func (n *MountNamespace) Wrap(cmd *exec.Cmd, uid, gid int, cwd string) {
	var namespaces []string
	for _, file := range []*os.File{n.mount, n.uts, n.ipc} {
		namespaces = append(namespaces, fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), file.Fd()))
	}
	args := []string{n.helper, EnterArg,
		strings.Join(namespaces, ","),
		fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), n.root.Fd()),
		strconv.Itoa(uid), strconv.Itoa(gid), cwd, "--", cmd.Path}
	cmd.Args = append(args, cmd.Args[1:]...)
	cmd.Path, cmd.Dir = n.helper, "/"
	// Encode entries individually to avoid one large environment value. In
	// particular LD_PRELOAD, GODEBUG and PATH must not affect trusted helpers.
	userEnv := cmd.Env
	if userEnv == nil {
		userEnv = os.Environ()
	}
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	for index, entry := range userEnv {
		cmd.Env = append(cmd.Env, workloadEnvPrefix+strconv.Itoa(index)+"="+entry)
	}
}

// RunMountHelper dispatches only trusted internal re-exec operations.
func RunMountHelper(args []string) error {
	if len(args) == 1 && args[0] == CaptureArg {
		ready, wait := os.NewFile(4, "ready"), os.NewFile(5, "release")
		defer ready.Close()
		defer wait.Close()
		_ = unix.Close(3)
		if err := json.NewEncoder(ready).Encode(os.Getpid()); err != nil {
			return err
		}
		_, err := io.Copy(io.Discard, wait)
		return err
	}
	if len(args) < 8 || args[0] != EnterArg || args[6] != "--" {
		return fmt.Errorf("invalid mount helper arguments")
	}
	uid, err := strconv.Atoi(args[3])
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(args[4])
	if err != nil {
		return err
	}
	var env []string
	for index := 0; ; index++ {
		value, ok := os.LookupEnv(workloadEnvPrefix + strconv.Itoa(index))
		if !ok {
			break
		}
		env = append(env, value)
	}
	// Open all handles in the host view; never look up a privileged helper or
	// library through caller-controlled rootfs paths. setns affects this thread.
	paths := strings.Split(args[1], ",")
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
	root, err := os.Open(args[2])
	if err != nil {
		return err
	}
	defer root.Close()
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
	if err := unix.Chroot("."); err != nil {
		return err
	}
	for _, handle := range handles {
		_ = handle.Close()
	}
	root.Close()
	return hostprivilege.ExecRestricted(uid, gid, args[5], args[7:], env)
}
