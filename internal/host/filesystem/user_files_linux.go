//go:build linux

package filesystem

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	hostprivilege "github.com/qiankunli/hostel/internal/host/privilege"
	hostprocess "github.com/qiankunli/hostel/internal/host/process"
	"golang.org/x/sys/unix"
)

const filesHelperArg = "__hostel_files"
const fileMessageLimit = 64 * 1024

// UserFiles owns a credential-restricted syscall worker. It has no listener or
// workload-provided environment: only the parent's inherited Unix socket is
// accepted. File contents use descriptors opened by this worker, never by the
// privileged caller. Only declared directory anchors may be supplied by the trusted parent; all file
// access beneath them happens after credentials have been dropped.
// +spec=`File access is checked by the kernel under the selected UID and groups, including ACL and symlink traversal; descriptor-relative confinement remains in force.`
// +link:doc=docs/volumes.md
type UserFiles struct {
	mu      sync.Mutex
	conn    *net.UnixConn
	command *exec.Cmd
	done    chan struct{}
	failed  error
	closed  bool
}

type fileRequest struct {
	Operation        string
	Root             int
	Path, Other      string
	Flags            int
	Mode             os.FileMode
	UID, GID         int
	Access, Modified time.Time
}
type fileMetadata struct {
	Filename    string
	Bytes       int64
	Permissions os.FileMode
	Modified    time.Time
	System      *syscall.Stat_t
}

func (m fileMetadata) Name() string       { return m.Filename }
func (m fileMetadata) Size() int64        { return m.Bytes }
func (m fileMetadata) Mode() os.FileMode  { return m.Permissions }
func (m fileMetadata) ModTime() time.Time { return m.Modified }
func (m fileMetadata) IsDir() bool        { return m.Permissions.IsDir() }
func (m fileMetadata) Sys() any           { return m.System }

type fileResponse struct {
	Root     int
	Text     string
	Metadata *fileMetadata
	Error    string
	Errno    syscall.Errno
}

func unixFileConn(file *os.File) (*net.UnixConn, error) {
	conn, err := net.FileConn(file)
	if err != nil {
		return nil, err
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		conn.Close()
		return nil, fmt.Errorf("filesystem worker requires Unix socket")
	}
	return unixConn, nil
}

func NewUserFiles(uid, gid int, groups []int) (*UserFiles, error) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(pair[0]), "filesystem-parent")
	child := os.NewFile(uintptr(pair[1]), "filesystem-child")
	defer parent.Close()
	defer child.Close()
	conn, err := unixFileConn(parent)
	if err != nil {
		return nil, err
	}
	command := exec.Command(hostprocess.Executable(), filesHelperArg)
	command.Dir = "/"
	command.Env = []string{"PATH=/usr/bin:/bin"}
	command.ExtraFiles = []*os.File{child}
	command.Stderr = os.Stderr
	if err := hostprivilege.WrapCredentials(command, uid, gid, groups); err != nil {
		conn.Close()
		return nil, err
	}
	if err := command.Start(); err != nil {
		conn.Close()
		return nil, err
	}
	worker := &UserFiles{conn: conn, command: command, done: make(chan struct{})}
	go func() { _ = command.Wait(); close(worker.done) }()
	if _, file, err := worker.call(fileRequest{Operation: "ping"}); err != nil {
		if file != nil {
			file.Close()
		}
		_ = worker.Close()
		return nil, fmt.Errorf("filesystem identity worker: %w", err)
	}
	return worker, nil
}

func receiveFileMessage(conn *net.UnixConn, value any) (*os.File, error) {
	data := make([]byte, fileMessageLimit)
	ancillary := make([]byte, unix.CmsgSpace(4))
	n, oob, flags, _, err := conn.ReadMsgUnix(data, ancillary)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, io.EOF
	}
	var file *os.File
	if oob > 0 {
		messages, err := unix.ParseSocketControlMessage(ancillary[:oob])
		if err != nil {
			return nil, err
		}
		for _, message := range messages {
			fds, err := unix.ParseUnixRights(&message)
			if err != nil {
				if file != nil {
					file.Close()
				}
				return nil, err
			}
			if len(fds) != 1 || file != nil {
				for _, fd := range fds {
					_ = unix.Close(fd)
				}
				if file != nil {
					_ = file.Close()
				}
				return nil, fmt.Errorf("unexpected descriptor count")
			}
			unix.CloseOnExec(fds[0])
			file = os.NewFile(uintptr(fds[0]), "filesystem-result")
		}
	}
	if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
		if file != nil {
			file.Close()
		}
		return nil, fmt.Errorf("filesystem message truncated")
	}
	if err := json.Unmarshal(data[:n], value); err != nil {
		if file != nil {
			file.Close()
		}
		return nil, err
	}
	return file, nil
}
func sendFileMessage(conn *net.UnixConn, value any, file *os.File) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > fileMessageLimit {
		return fmt.Errorf("filesystem message too large")
	}
	var rights []byte
	if file != nil {
		rights = unix.UnixRights(int(file.Fd()))
	}
	n, _, err := conn.WriteMsgUnix(data, rights, nil)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}

func (w *UserFiles) call(request fileRequest) (fileResponse, *os.File, error) {
	return w.callWithFile(request, nil)
}
func (w *UserFiles) callWithFile(request fileRequest, anchor *os.File) (fileResponse, *os.File, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var response fileResponse
	if w.closed {
		return response, nil, os.ErrClosed
	}
	if w.failed != nil {
		return response, nil, w.failed
	}
	// A transport failure loses request/reply alignment. Never reuse that socket.
	fail := func(err error) (fileResponse, *os.File, error) {
		w.failed = err
		_ = w.conn.Close()
		_ = w.command.Process.Kill()
		return response, nil, err
	}
	if err := w.conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return fail(err)
	}
	if err := sendFileMessage(w.conn, request, anchor); err != nil {
		return fail(err)
	}
	file, err := receiveFileMessage(w.conn, &response)
	if err != nil {
		return fail(err)
	}
	if response.Error != "" {
		if file != nil {
			file.Close()
		}
		cause := error(errors.New(response.Error))
		if response.Errno != 0 {
			cause = response.Errno
		}
		return response, nil, &os.PathError{Op: request.Operation, Path: request.Path, Err: cause}
	}
	return response, file, nil
}

func (w *UserFiles) Close() error {
	w.mu.Lock()
	w.closed = true
	_ = w.conn.Close()
	w.mu.Unlock()
	select {
	case <-w.done:
		return nil
	case <-time.After(time.Second):
	}
	_ = w.command.Process.Kill()
	select {
	case <-w.done:
		return nil
	case <-time.After(time.Second):
		return fmt.Errorf("filesystem worker did not exit")
	}
}

func init() {
	if len(os.Args) < 2 || os.Args[1] != filesHelperArg {
		return
	}
	if err := serveUserFiles(); err != nil {
		fmt.Fprintln(os.Stderr, "hostel filesystem worker:", err)
		os.Exit(1)
	}
	os.Exit(0)
}
func serveUserFiles() error {
	socket := os.NewFile(3, "filesystem-worker")
	conn, err := unixFileConn(socket)
	socket.Close()
	if err != nil {
		return err
	}
	defer conn.Close()
	roots := map[int]*os.Root{}
	defer func() {
		for _, root := range roots {
			root.Close()
		}
	}()
	next := 0
	for {
		var request fileRequest
		anchor, err := receiveFileMessage(conn, &request)
		if anchor != nil && request.Operation != "bind_root" {
			anchor.Close()
			return fmt.Errorf("unexpected request descriptor")
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var response fileResponse
		var file *os.File
		var info os.FileInfo
		root := roots[request.Root]
		if request.Operation == "ping" {
			response.Text = "ready"
		} else if request.Operation == "new_root" || request.Operation == "bind_root" {
			var opened *os.Root
			if request.Operation == "bind_root" {
				if anchor == nil {
					err = syscall.EBADF
				} else {
					opened, err = os.OpenRoot(fmt.Sprintf("/proc/self/fd/%d", anchor.Fd()))
					anchor.Close()
				}
			} else {
				opened, err = os.OpenRoot(request.Path)
			}
			if err == nil {
				next++
				roots[next] = opened
				response.Root = next
			}
		} else if root == nil {
			err = syscall.EBADF
		} else {
			switch request.Operation {
			case "open_root":
				var opened *os.Root
				opened, err = root.OpenRoot(request.Path)
				if err == nil {
					next++
					roots[next] = opened
					response.Root = next
				}
			case "close":
				err = root.Close()
				delete(roots, request.Root)
			case "open":
				file, err = root.OpenFile(request.Path, request.Flags|unix.O_NONBLOCK, request.Mode)
				if err == nil {
					info, err = file.Stat()
					if err == nil && !info.IsDir() && !info.Mode().IsRegular() {
						err = syscall.EOPNOTSUPP
					}
				}
			case "stat":
				info, err = root.Stat(request.Path)
			case "lstat":
				info, err = root.Lstat(request.Path)
			case "mkdir":
				err = root.Mkdir(request.Path, request.Mode)
			case "mkdir_all":
				err = root.MkdirAll(request.Path, request.Mode)
			case "remove":
				err = root.Remove(request.Path)
			case "remove_all":
				err = root.RemoveAll(request.Path)
			case "rename":
				err = root.Rename(request.Path, request.Other)
			case "link":
				err = root.Link(request.Path, request.Other)
			case "symlink":
				err = root.Symlink(request.Path, request.Other)
			case "readlink":
				response.Text, err = root.Readlink(request.Path)
			case "chmod":
				err = root.Chmod(request.Path, request.Mode)
			case "lchown":
				err = root.Lchown(request.Path, request.UID, request.GID)
			case "chtimes":
				err = root.Chtimes(request.Path, request.Access, request.Modified)
			default:
				err = syscall.EINVAL
			}
		}
		if err != nil {
			response.Error = err.Error()
			_ = errors.As(err, &response.Errno)
			if file != nil {
				file.Close()
				file = nil
			}
		} else if info != nil {
			stat, _ := info.Sys().(*syscall.Stat_t)
			response.Metadata = &fileMetadata{Filename: info.Name(), Bytes: info.Size(), Permissions: info.Mode(), Modified: info.ModTime(), System: stat}
		}
		sendErr := sendFileMessage(conn, response, file)
		if file != nil {
			file.Close()
		}
		if sendErr != nil {
			return sendErr
		}
	}
}
