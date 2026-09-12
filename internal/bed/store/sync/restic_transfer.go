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
	"io/fs"
	"os"
	"path/filepath"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	"github.com/qiankunli/hostel/internal/tracing"
)

// Transfer executes validated directory-transfer options, joining restic and
// cleaning staging before returning so the caller can safely release its Bed pin.
func (s *Restic) Transfer(ctx context.Context, fs *bedfs.FS, req TransferOptions, completed func(int64)) (string, int64, int64, error) {
	staging, err := os.MkdirTemp("", "hostel-transfer-*")
	if err != nil {
		return "", 0, 0, transferErrorAt(err, "stage", ".")
	}
	defer cleanupResticTree(ctx, staging)
	if req.Source.Type == "bed" {
		if err := fs.ExportTransferTree(ctx, req.Source.Path, staging, nil); err != nil {
			return "", 0, 0, resticTreeError(err, "scan")
		}
		summary, err := s.upload(ctx, s.repository(req.Destination.Key), staging, req.ParentRef)
		if err != nil {
			return "", 0, 0, err
		}
		return summary.SnapshotID, summary.Files, summary.Bytes, nil
	}
	if err := s.download(ctx, s.repository(req.Source.Key), req.Source.Ref, staging); err != nil {
		return "", 0, 0, err
	}
	err = fs.ImportTransferTree(ctx, staging, req.Destination.Path, req.Overwrite, completed)
	return req.Source.Ref, 0, 0, resticTreeError(err, "publish")
}

func resticTreeError(err error, stage string) error {
	relative := "."
	var pathErr *os.PathError
	if errors.As(err, &pathErr) && pathErr.Op == "transfer tree" {
		relative = pathErr.Path
	}
	if errors.Is(err, bedfs.ErrTransferFileType) {
		err = ErrTransferInvalid
	}
	if errors.Is(err, os.ErrExist) {
		err = ErrTransferConflict
	}
	return transferErrorAt(err, stage, relative)
}

func cleanupResticTree(ctx context.Context, directory string) {
	_ = filepath.WalkDir(directory, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(name, 0o700)
		}
		return nil
	})
	if err := os.RemoveAll(directory); err != nil {
		tracing.WarnContext(ctx, "hostel transfer staging cleanup failed", "stage", "cleanup", "error", Failure(err))
	}
}
