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
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/qiankunli/go-stdx/randx"
	"golang.org/x/sys/unix"
)

// ExportTransferTree copies a directory through anchored BedFS reads into a
// private staging directory. Format tools never traverse the live Bed's host
// path, where a concurrent symlink swap could otherwise escape its boundary.
// excluded, when non-nil, receives source-relative paths (automatic durability).
func (o *FS) ExportTransferTree(ctx context.Context, source, directory string, excluded func(string) bool) error {
	full, err := o.Resolve(source)
	if err != nil {
		return err
	}
	rel, err := o.relative(full)
	if err != nil {
		return err
	}
	src, err := o.root.OpenRoot(rel)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer dst.Close()
	return copyTransferTree(ctx, src, dst, false, excluded, nil, nil)
}

// ImportTransferTree publishes files from a private staging tree into BedFS.
// Each entry is published atomically; unrelated destination entries remain.
// Regular permissions, modification times, empty directories and confined
// relative symlinks are preserved. File ownership belongs to the receiving Bed.
func (o *FS) ImportTransferTree(ctx context.Context, directory, destination string, overwrite bool, completed func(int64)) error {
	full, err := o.Resolve(destination)
	if err != nil {
		return err
	}
	if err := o.mkdirAllOwned(full); err != nil {
		return err
	}
	rel, err := o.relative(full)
	if err != nil {
		return err
	}
	dst, err := o.root.OpenRoot(rel)
	if err != nil {
		return err
	}
	defer dst.Close()
	src, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer src.Close()
	owned := func(name string) { o.chownNew(filepath.Join(full, filepath.FromSlash(name))) }
	return copyTransferTree(ctx, src, dst, overwrite, nil, owned, completed)
}

func copyTransferTree(ctx context.Context, src, dst *os.Root, overwrite bool, excluded func(string) bool, owned func(string), completed func(int64)) error {
	type directory struct {
		name string
		info os.FileInfo
	}
	var dirs []directory
	err := fs.WalkDir(src.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) (result error) {
		defer func() {
			if result != nil && !errors.Is(result, fs.SkipDir) {
				result = &os.PathError{Op: "transfer tree", Path: name, Err: result}
			}
		}()
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if excluded != nil && excluded(name) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := src.Lstat(name)
		if err != nil {
			return err
		}
		if info.IsDir() {
			err := dst.Mkdir(name, 0o755)
			if err != nil {
				if !errors.Is(err, os.ErrExist) {
					return err
				}
				existing, err := dst.Lstat(name)
				if err != nil {
					return err
				}
				if !existing.IsDir() {
					return ErrTransferFileType
				}
			} else {
				dirs = append(dirs, directory{name, info})
			}
			if owned != nil {
				owned(name)
			}
			return nil
		}
		temp := path.Join(path.Dir(name), ".hostel-transfer-"+randx.Hex(16))
		defer dst.Remove(temp)
		var size int64
		switch {
		case info.Mode().IsRegular():
			input, err := src.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
			if err != nil {
				return err
			}
			defer input.Close()
			info, err = input.Stat()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return ErrTransferFileType
			}
			output, err := dst.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			size, err = io.Copy(output, transferReader{ctx: ctx, reader: input})
			if err == nil {
				err = output.Chmod(info.Mode().Perm())
			}
			err = errors.Join(err, output.Close())
			if err != nil {
				return err
			}
			if err := dst.Chtimes(temp, info.ModTime(), info.ModTime()); err != nil {
				return err
			}
		case info.Mode()&os.ModeSymlink != 0:
			link, err := src.Readlink(name)
			if err != nil {
				return err
			}
			resolved := path.Join(path.Dir(name), filepath.ToSlash(link))
			if filepath.IsAbs(link) || resolved == ".." || strings.HasPrefix(resolved, "../") {
				return ErrTransferFileType
			}
			if err := dst.Symlink(link, temp); err != nil {
				return err
			}
		default:
			return ErrTransferFileType
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if owned != nil {
			owned(temp)
		}
		if overwrite {
			err = dst.Rename(temp, name)
		} else {
			err = dst.Link(temp, name)
		}
		if err != nil {
			return err
		}
		if completed != nil && info.Mode().IsRegular() {
			completed(size)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Apply directory modes last so a read-only source directory does not block
	// construction of its children. Existing destination directory modes remain.
	for i := len(dirs) - 1; i >= 0; i-- {
		d := dirs[i]
		if err := dst.Chmod(d.name, d.info.Mode().Perm()); err != nil {
			return err
		}
		if err := dst.Chtimes(d.name, d.info.ModTime(), d.info.ModTime()); err != nil {
			return err
		}
	}
	return nil
}
