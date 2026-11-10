package filesystem

import (
	"io"
	"io/fs"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// Root performs daemon-authority path operations in one anchored directory.
// The caller owns admission, data scope and read-only policy.
type Root interface {
	Close() error
	OpenRoot(string) (Root, error)
	Open(string) (*os.File, error)
	OpenFile(string, int, os.FileMode) (*os.File, error)
	Stat(string) (os.FileInfo, error)
	Lstat(string) (os.FileInfo, error)
	Mkdir(string, os.FileMode) error
	MkdirAll(string, os.FileMode) error
	Remove(string) error
	RemoveAll(string) error
	Rename(string, string) error
	Link(string, string) error
	Symlink(string, string) error
	Readlink(string) (string, error)
	Chmod(string, os.FileMode) error
	Lchown(string, int, int) error
	Chtimes(string, time.Time, time.Time) error
	ReadFile(string) ([]byte, error)
	WriteFile(string, []byte, os.FileMode) error
	FS() fs.FS
}

type localRoot struct{ *os.Root }

func OpenRoot(path string) (Root, error) {
	r, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return localRoot{r}, nil
}
func (r localRoot) OpenRoot(path string) (Root, error) {
	child, err := r.Root.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return localRoot{child}, nil
}

// FS routes directory walks through the same checked opens as direct reads.
func (r localRoot) FS() fs.FS { return rootFS{r} }

type rootFS struct{ root Root }

func (r rootFS) Open(path string) (fs.File, error) {
	if !fs.ValidPath(path) {
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrInvalid}
	}
	return r.root.Open(path)
}

// OpenFile preserves the file API's ordinary-file boundary without a worker.
// Delay truncation until after checking the descriptor, so special files cannot
// block a request or be used as daemon-authority device I/O.
func (r localRoot) OpenFile(path string, flags int, mode os.FileMode) (*os.File, error) {
	f, err := r.Root.OpenFile(path, (flags&^os.O_TRUNC)|unix.O_NONBLOCK, mode)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil && !info.IsDir() && !info.Mode().IsRegular() {
		err = &os.PathError{Op: "open", Path: path, Err: unix.EOPNOTSUPP}
	}
	if err == nil && flags&os.O_TRUNC != 0 {
		err = f.Truncate(0)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func (r localRoot) Open(path string) (*os.File, error) { return r.OpenFile(path, os.O_RDONLY, 0) }
func (r localRoot) ReadFile(path string) ([]byte, error) {
	f, err := r.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
func (r localRoot) WriteFile(path string, data []byte, mode os.FileMode) error {
	f, err := r.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
