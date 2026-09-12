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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
)

type resticStore struct {
	obj     objects
	prefix  string
	command *Restic
	filter  snapshotFilter
}

type resticHead struct {
	Ref        string `json:"ref"`
	Generation int64  `json:"generation"`
	Bytes      int64  `json:"bytes"`
}

func (s *resticStore) Name() Kind                 { return KindRestic }
func (s *resticStore) bedPrefix(id string) string { return path.Join(s.prefix, "restic", id) + "/" }
func (s *resticStore) headKey(id string) string   { return s.bedPrefix(id) + "head.json" }
func (s *resticStore) repository(id string) string {
	return s.command.repository(path.Join("restic", id, "repository"))
}

func (s *resticStore) Stat(ctx context.Context, id string) (*SnapshotInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, objectOpTimeout)
	defer cancel()
	meta, _, exists, err := s.obj.Head(ctx, s.headKey(id))
	if err != nil || !exists {
		return nil, err
	}
	generation, err := strconv.ParseInt(meta[generationMetaKey], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("restic head: invalid generation: %w", err)
	}
	size, _ := strconv.ParseInt(meta["bytes"], 10, 64)
	return &SnapshotInfo{Generation: generation, Bytes: size}, nil
}

func (s *resticStore) readHead(ctx context.Context, id string) (resticHead, error) {
	body, err := s.obj.Get(ctx, s.headKey(id))
	if err != nil {
		return resticHead{}, err
	}
	defer body.Close()
	var head resticHead
	if err := json.NewDecoder(io.LimitReader(body, 64<<10)).Decode(&head); err != nil {
		return head, err
	}
	if !resticRefPattern.MatchString(head.Ref) {
		return head, fmt.Errorf("restic head: invalid reference")
	}
	return head, nil
}

func (s *resticStore) Persist(ctx context.Context, id, dir string, generation int64) error {
	ctx, cancel := context.WithTimeout(ctx, tarOpTimeout)
	defer cancel()
	if err := s.command.Available(ctx); err != nil {
		return err
	}
	info, err := s.Stat(ctx, id)
	if err != nil {
		return err
	}
	var parent string
	if info != nil {
		if info.Generation >= generation {
			return ErrConflict
		}
		head, err := s.readHead(ctx, id)
		if err != nil {
			return err
		}
		parent = head.Ref
	}
	staging, err := os.MkdirTemp("", "hostel-restic-*")
	if err != nil {
		return err
	}
	defer cleanupResticTree(ctx, staging)
	// Automatic persistence applies its durability allowlist before handing a
	// private tree to the format tool. Explicit transfers have no such allowlist.
	source, err := bedfs.New(dir)
	if err != nil {
		return err
	}
	defer source.Close()
	if err := source.ExportTransferTree(ctx, "/", staging, s.filter.excluded); err != nil {
		return err
	}
	summary, err := s.command.upload(ctx, s.repository(id), staging, parent)
	if err != nil {
		return err
	}
	head := resticHead{Ref: summary.SnapshotID, Generation: generation, Bytes: summary.Bytes}
	data, err := json.Marshal(head)
	if err != nil {
		return err
	}
	return s.obj.Put(ctx, s.headKey(id), bytes.NewReader(data), int64(len(data)), map[string]string{generationMetaKey: strconv.FormatInt(generation, 10), "bytes": strconv.FormatInt(summary.Bytes, 10)})
}

func (s *resticStore) Restore(ctx context.Context, id, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, tarOpTimeout)
	defer cancel()
	if err := s.command.Available(ctx); err != nil {
		return err
	}
	head, err := s.readHead(ctx, id)
	if err != nil {
		return err
	}
	return s.command.download(ctx, s.repository(id), head.Ref, dir)
}

func (s *resticStore) Delete(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, objectOpTimeout)
	defer cancel()
	keys, err := s.obj.List(ctx, s.bedPrefix(id))
	if err != nil {
		return err
	}
	return s.obj.Delete(ctx, keys)
}
