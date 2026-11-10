package filesystem

import (
	"io"
	"io/fs"
	"os"
	"time"
)

// Root performs path operations in one anchored directory. A credential-bound
// implementation executes every syscall as the Bed user; local roots are for
// trusted preparation and persistence. Callers must not mutate returned file
// metadata with daemon credentials.
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

type rootFS struct{ root Root }

func (r rootFS) Open(path string) (fs.File, error) {
	if !fs.ValidPath(path) {
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrInvalid}
	}
	return r.root.Open(path)
}
func readRootFile(root Root, path string) ([]byte, error) {
	f, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
func writeRootFile(root Root, path string, data []byte, mode os.FileMode) error {
	f, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
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
