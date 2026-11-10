//go:build linux

package filesystem

import (
	"io/fs"
	"os"
	"sync"
	"time"
)

type userRoot struct {
	worker    *UserFiles
	id        int
	closeOnce sync.Once
	closeErr  error
}

func (w *UserFiles) OpenRoot(path string) (Root, error) {
	reply, _, err := w.call(fileRequest{Operation: "new_root", Path: path})
	if err != nil {
		return nil, err
	}
	return &userRoot{worker: w, id: reply.Root}, nil
}
func (r *userRoot) request(req fileRequest) (fileResponse, *os.File, error) {
	req.Root = r.id
	return r.worker.call(req)
}
func (r *userRoot) Close() error {
	r.closeOnce.Do(func() {
		_, _, r.closeErr = r.request(fileRequest{Operation: "close"})
		r.worker.mu.Lock()
		if r.worker.closed || r.worker.failed != nil {
			r.closeErr = nil
		}
		r.worker.mu.Unlock()
	})
	return r.closeErr
}
func (r *userRoot) OpenRoot(path string) (Root, error) {
	reply, _, err := r.request(fileRequest{Operation: "open_root", Path: path})
	if err != nil {
		return nil, err
	}
	return &userRoot{worker: r.worker, id: reply.Root}, nil
}
func (r *userRoot) Open(path string) (*os.File, error) { return r.OpenFile(path, os.O_RDONLY, 0) }
func (r *userRoot) OpenFile(path string, flags int, mode os.FileMode) (*os.File, error) {
	_, file, err := r.request(fileRequest{Operation: "open", Path: path, Flags: flags, Mode: mode})
	return file, err
}
func (r *userRoot) Stat(path string) (os.FileInfo, error) {
	reply, _, err := r.request(fileRequest{Operation: "stat", Path: path})
	if err != nil {
		return nil, err
	}
	return reply.Metadata, nil
}
func (r *userRoot) Lstat(path string) (os.FileInfo, error) {
	reply, _, err := r.request(fileRequest{Operation: "lstat", Path: path})
	if err != nil {
		return nil, err
	}
	return reply.Metadata, nil
}
func (r *userRoot) Mkdir(path string, mode os.FileMode) error {
	_, _, err := r.request(fileRequest{Operation: "mkdir", Path: path, Mode: mode})
	return err
}
func (r *userRoot) MkdirAll(path string, mode os.FileMode) error {
	_, _, err := r.request(fileRequest{Operation: "mkdir_all", Path: path, Mode: mode})
	return err
}
func (r *userRoot) Remove(path string) error {
	_, _, err := r.request(fileRequest{Operation: "remove", Path: path})
	return err
}
func (r *userRoot) RemoveAll(path string) error {
	_, _, err := r.request(fileRequest{Operation: "remove_all", Path: path})
	return err
}
func (r *userRoot) Rename(path, other string) error {
	_, _, err := r.request(fileRequest{Operation: "rename", Path: path, Other: other})
	return err
}
func (r *userRoot) Link(path, other string) error {
	_, _, err := r.request(fileRequest{Operation: "link", Path: path, Other: other})
	return err
}
func (r *userRoot) Symlink(path, other string) error {
	_, _, err := r.request(fileRequest{Operation: "symlink", Path: path, Other: other})
	return err
}
func (r *userRoot) Readlink(path string) (string, error) {
	reply, _, err := r.request(fileRequest{Operation: "readlink", Path: path})
	return reply.Text, err
}
func (r *userRoot) Chmod(path string, mode os.FileMode) error {
	_, _, err := r.request(fileRequest{Operation: "chmod", Path: path, Mode: mode})
	return err
}
func (r *userRoot) Lchown(path string, uid, gid int) error {
	_, _, err := r.request(fileRequest{Operation: "lchown", Path: path, UID: uid, GID: gid})
	return err
}
func (r *userRoot) Chtimes(path string, atime, mtime time.Time) error {
	_, _, err := r.request(fileRequest{Operation: "chtimes", Path: path, Access: atime, Modified: mtime})
	return err
}
func (r *userRoot) ReadFile(path string) ([]byte, error) { return readRootFile(r, path) }
func (r *userRoot) WriteFile(path string, data []byte, mode os.FileMode) error {
	return writeRootFile(r, path, data, mode)
}
func (r *userRoot) FS() fs.FS { return rootFS{root: r} }

// BindRoot transfers only the already-authorized directory anchor. Like a
// volume mount, it does not require the Bed to traverse Carrier-only ancestors.
// Every file open or metadata mutation below it still runs under Bed credentials.
func (w *UserFiles) BindRoot(root Root) (Root, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	reply, _, err := w.callWithFile(fileRequest{Operation: "bind_root"}, dir)
	if err != nil {
		return nil, err
	}
	return &userRoot{worker: w, id: reply.Root}, nil
}
