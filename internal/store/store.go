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

// Package store persists bed workspaces beyond the life of the process/pod
// (docs/store.md): the durable identity of a bed is a snapshot in object
// storage; the local workspace is just its working copy, synced at lifecycle
// boundaries (create/resume ← restore, idle/delete/checkpoint → persist).
// hostel does not solve multi-writer coordination — "one bedID live in one
// hostel at a time" is the upstream scheduler's guarantee.
package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

// ErrConflict reports that the backend already holds a snapshot at least as
// new as the one being persisted: another hostel instance has persisted this
// bed since we initialized it (dual-initialization — the upstream scheduler's
// single-writer guarantee was violated). First writer wins: overwriting would
// silently drop the other instance's data, which is strictly worse than
// failing loudly. Callers must not blindly retry; the bed needs re-initialization
// from the newer snapshot (docs/store.md §3.5).
var ErrConflict = errors.New("snapshot conflict: concurrent writer detected")

// SnapshotInfo describes a bed's durable snapshot without downloading it.
type SnapshotInfo struct {
	// Generation is the snapshot's persist counter (mirrors the bed meta's
	// generation, carried in backend metadata so Stat stays cheap). A local
	// copy with generation >= this is current and can skip Restore.
	Generation int64
	// Bytes is the packed snapshot size (0 when the backend can't tell).
	Bytes int64
}

// Store is the persistence backend for bed workspaces. Implementations must
// treat Persist as atomic per bed (a reader never sees a half-written
// snapshot) — a single commit-point object per bed gives this on S3.
type Store interface {
	// Name reports the backend for capabilities/healthz ("noop", "auto", "s3", "pack", "tar").
	Name() string
	// Stat describes the bed's snapshot, or nil when none exists. Must be
	// cheap (S3: HEAD + user metadata, no download) — luggage freshness
	// checks call it on every resume.
	Stat(ctx context.Context, bedID string) (*SnapshotInfo, error)
	// Restore unpacks the bed's snapshot into dir (an existing, usually empty
	// workspace dir). Called on bed create/resume, before serving requests.
	Restore(ctx context.Context, bedID, dir string) error
	// Persist snapshots dir as the bed's durable copy, replacing any previous
	// snapshot. Called on evict, explicit checkpoint, and the periodic safety
	// net. dir is the bed dir (portable meta + data/); top-level *.local
	// files are host-private and excluded; data/tmp is the bed_home's ephemeral
	// /tmp and is excluded recursively. generation is the meta's persist counter,
	// surfaced back through Stat.
	Persist(ctx context.Context, bedID, dir string, generation int64) error
	// Delete removes the bed's snapshot — the purge path: after this the bed
	// identity no longer exists anywhere. Deleting a missing snapshot is not
	// an error.
	Delete(ctx context.Context, bedID string) error
}

// Config selects and parameterizes the backend (flags/env in config package).
type Config struct {
	Backend         string // "auto" (default) | "noop" | "s3" ("cas" alias) | "pack" | "tar"
	Bucket          string
	Prefix          string // key prefix inside the bucket, e.g. "hostel/prod"
	Endpoint        string // non-AWS S3-compatible endpoint (MinIO/TOS/Ceph); "" = AWS
	PathStyle       bool   // force path-style bucket addressing; default is virtual-hosted style
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	// AutoPackFileThreshold switches an auto-routed CAS bed to pack when the
	// persisted tree contains more than this many non-directory entries.
	// Zero disables the automatic transition.
	AutoPackFileThreshold int
}

// snapshotPathExcluded is shared by every concrete snapshot format so changing
// backends cannot accidentally make carrier-private or ephemeral paths durable.
func snapshotPathExcluded(rel string) bool {
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	if rel == "data/tmp" || strings.HasPrefix(rel, "data/tmp/") {
		return true
	}
	return !strings.Contains(rel, "/") && strings.HasSuffix(filepath.Base(rel), ".local")
}
