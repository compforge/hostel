package isolation

import (
	"context"

	"github.com/qiankunli/hostel/internal/bedfs"
	"github.com/qiankunli/hostel/internal/lifecycle"
)

// FilesBinding pins preparation and close to one BedFS allocation.
type FilesBinding struct {
	lifecycle.Noop
	files Isolator
	fs    *bedfs.FS
}

var _ lifecycle.Component[WorkspaceViewReport] = (*FilesBinding)(nil)

func BindFiles(files Isolator, fs *bedfs.FS) *FilesBinding {
	return &FilesBinding{files: files, fs: fs}
}
func (b *FilesBinding) Prepare(context.Context) error {
	if p, ok := b.files.(Preparer); ok {
		return p.Prepare(b.fs)
	}
	return nil
}
func (b *FilesBinding) Release(context.Context) error { return b.fs.Close() }
func (b *FilesBinding) Diagnostics() WorkspaceViewReport {
	if r, ok := b.files.(Report); ok {
		return r.WorkspaceView()
	}
	if b.files.WorkspaceMounted() {
		return WorkspaceViewReport{Mode: "mount", Available: true}
	}
	return WorkspaceViewReport{Mode: "carrier", Available: true}
}
