package isolation

import (
	"context"
	"io"
	"os/exec"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
)

type processViewBinder interface {
	bindExecutorView(context.Context, *bedfs.FS, func(*exec.Cmd) error) (func(*exec.Cmd), io.Closer, error)
}

type executorView struct {
	Isolator
	enter func(*exec.Cmd)
}

func (v *executorView) Wrap(cmd *exec.Cmd, _ *bedfs.FS, _ string) error { v.enter(cmd); return nil }

// BindExecutorView derives retained mounts in a caller-owned process realm.
// The caller must wait/reap every process launched by start, even on failure.
// Other mechanisms already inherit the realm's procfs and need no retained copy.
func BindExecutorView(ctx context.Context, files Isolator, fs *bedfs.FS, start func(*exec.Cmd) error) (Isolator, io.Closer, error) {
	var boundary Boundary = files
	if r, ok := files.(*resolved); ok {
		boundary = r.boundary
	}
	binder, ok := boundary.(processViewBinder)
	if !ok {
		return files, nil, nil
	}
	enter, closer, err := binder.bindExecutorView(ctx, fs, start)
	if err != nil {
		return nil, nil, err
	}
	if enter == nil {
		return files, nil, nil
	}
	return &executorView{Isolator: files, enter: enter}, closer, nil
}
