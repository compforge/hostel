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

package store

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"strings"

	"github.com/qiankunli/hostel/internal/bedfs"
)

// transferObjects is the streaming, conditional-write subset used by copies.
// Snapshot strategies continue to own their metadata and layout in objAPI.
type transferObjects interface {
	get(context.Context, string) (io.ReadCloser, error)
	visit(context.Context, string, func(string) error) error
	upload(context.Context, string, *os.File, int64, bool) error
}

func copyTransfer(ctx context.Context, obj transferObjects, prefix string, fs *bedfs.FS, req TransferRequest, completed func(int64)) error {
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
			callCtx, cancel := context.WithTimeout(ctx, s3OpTimeout)
			defer cancel()
			if err := obj.upload(callCtx, key, file, size, req.Overwrite); err != nil {
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
		callCtx, cancel := context.WithTimeout(ctx, s3OpTimeout)
		defer cancel()
		body, err := obj.get(callCtx, key)
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
	err := obj.visit(ctx, source, func(key string) error {
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

// transferCopyError carries only the stage and path relative to the requested
// directory. It preserves error identity for cancellation and API categories.
type transferCopyError struct {
	stage, relative string
	err             error
}

func (e *transferCopyError) Error() string { return e.err.Error() }
func (e *transferCopyError) Unwrap() error { return e.err }

func transferErrorAt(err error, stage, relative string) error {
	if err == nil {
		return nil
	}
	var existing *transferCopyError
	if errors.As(err, &existing) {
		return err
	}
	return &transferCopyError{stage: stage, relative: relative, err: err}
}
