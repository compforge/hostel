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

package sync

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"strings"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
)

// transferObjects is the streaming, conditional-write subset used by copies.
// Snapshot strategies continue to own their metadata and layout in objects.
type transferObjects interface {
	Get(context.Context, string) (io.ReadCloser, error)
	Visit(context.Context, string, func(string) error) error
	Upload(context.Context, string, *os.File, int64, bool) error
}

// Copy executes validated transfer options; completed reports each fully published file.
func Copy(ctx context.Context, obj transferObjects, prefix string, fs *bedfs.FS, req TransferOptions, completed func(int64)) error {
	if req.Source.Type == "bed" {
		info, err := fs.Stat(req.Source.Path)
		if err != nil {
			return transferErrorAt(err, "scan", ".")
		}
		if info.Type != "directory" && strings.HasSuffix(req.Destination.Key, "/") {
			return transferErrorAt(ErrTransferInvalid, "scan", ".")
		}
		err = fs.WalkTransferFiles(ctx, req.Source.Path, func(relative string, file *os.File, size int64) error {
			key := path.Join(prefix, req.Destination.Key)
			if relative != "." {
				key = path.Join(key, relative)
			}
			callCtx, cancel := context.WithTimeout(ctx, objectOpTimeout)
			defer cancel()
			if err := obj.Upload(callCtx, key, file, size, req.Overwrite); err != nil {
				return transferErrorAt(err, "upload", relative)
			}
			completed(size)
			return nil
		})
		relative := "."
		var pathErr *os.PathError
		if errors.As(err, &pathErr) && pathErr.Op == "scan" {
			relative = pathErr.Path
		}
		if errors.Is(err, bedfs.ErrTransferFileType) {
			err = ErrTransferInvalid
		}
		return transferErrorAt(err, "scan", relative)
	}
	source := path.Join(prefix, req.Source.Key)
	copyFile := func(key, destination, relative string) error {
		callCtx, cancel := context.WithTimeout(ctx, objectOpTimeout)
		defer cancel()
		body, err := obj.Get(callCtx, key)
		if err != nil {
			return transferErrorAt(err, "download", relative)
		}
		bytes, err := fs.WriteTransferFile(callCtx, destination, body, req.Overwrite)
		err = errors.Join(err, body.Close())
		if errors.Is(err, os.ErrExist) {
			err = ErrTransferConflict
		}
		if err != nil {
			return transferErrorAt(err, "download", relative)
		}
		completed(bytes)
		return nil
	}
	if !strings.HasSuffix(req.Source.Key, "/") {
		return copyFile(source, req.Destination.Path, ".")
	}
	source += "/"
	err := obj.Visit(ctx, source, func(key string) error {
		if !strings.HasPrefix(key, source) {
			return ErrTransferInvalid
		}
		relative := strings.TrimPrefix(key, source)
		if relative == "" {
			return nil
		} // S3 directory marker, no file payload.
		if !validTransferKey(relative) {
			return ErrTransferInvalid
		}
		if strings.HasSuffix(relative, "/") {
			return nil
		}
		return copyFile(key, path.Join(req.Destination.Path, relative), relative)
	})
	return transferErrorAt(err, "list", ".")
}

// OperationError carries only the stage and path relative to the requested
// directory. It preserves error identity for cancellation and API categories.
type OperationError struct {
	Stage, Relative string
	err             error
}

func (e *OperationError) Error() string { return e.err.Error() }
func (e *OperationError) Unwrap() error { return e.err }

func transferErrorAt(err error, stage, relative string) error {
	if err == nil {
		return nil
	}
	var existing *OperationError
	if errors.As(err, &existing) {
		return err
	}
	return &OperationError{Stage: stage, Relative: relative, err: err}
}
