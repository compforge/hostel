// Copyright 2026 Li Qiankun
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bedfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/qiankunli/go-stdx/randx"
	"golang.org/x/sys/unix"
)

var ErrTransferFileType = errors.New("copy supports regular files and directories only")

// WalkTransferFiles streams regular source files within the anchored BedFS.
// Symlinks and special files are rejected; a raced-in FIFO cannot block Open.
func (o *FS) WalkTransferFiles(ctx context.Context, source string, visit func(relative string, file *os.File, size int64) error) error {
	full, err := o.Resolve(source)
	if err != nil {
		return err
	}
	rel, err := o.relative(full)
	if err != nil {
		return err
	}
	return fs.WalkDir(o.root.FS(), filepath.ToSlash(rel), func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// WalkDir uses Stat for its starting path; inspect the entry itself
		// so a source symlink is rejected just like a nested symlink.
		info, err := o.root.Lstat(name)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrTransferFileType
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return ErrTransferFileType
		}
		file, err := o.root.OpenFile(name, os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		defer file.Close()
		info, err = file.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return ErrTransferFileType
		}
		relative, err := filepath.Rel(rel, name)
		if err != nil {
			return err
		}
		return visit(filepath.ToSlash(relative), file, info.Size())
	})
}

// WriteTransferFile publishes a fully downloaded file. Link provides atomic
// create-if-absent; Rename replaces the target entry without following it.
func (o *FS) WriteTransferFile(ctx context.Context, destination string, body io.Reader, overwrite bool) (int64, error) {
	full, err := o.Resolve(destination)
	if err != nil {
		return 0, err
	}
	if err := o.mkdirAllOwned(filepath.Dir(full)); err != nil {
		return 0, err
	}
	rel, err := o.relative(full)
	if err != nil {
		return 0, err
	}
	temp := filepath.Join(filepath.Dir(rel), ".hostel-transfer-"+randx.Hex(16))
	file, err := o.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	defer o.root.Remove(temp)
	written, copyErr := io.Copy(file, transferReader{ctx: ctx, reader: body})
	if copyErr == nil {
		copyErr = file.Chmod(0o644)
	}
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr, ctx.Err()); err != nil {
		return 0, err
	}
	o.chownNew(filepath.Join(o.Home(), temp))
	if overwrite {
		err = o.root.Rename(temp, rel)
	} else {
		err = o.root.Link(temp, rel)
	}
	if err != nil {
		return 0, fmt.Errorf("publish copied file: %w", err)
	}
	return written, nil
}

type transferReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r transferReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
