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

// Package store manages automatic Bed persistence and explicit file transfers.
package store

import (
	"github.com/qiankunli/hostel/internal/bed/store/backend"
	storesync "github.com/qiankunli/hostel/internal/bed/store/sync"
)

// +why=`The manager exposes policy contracts; implementations never import their owner.`
type Store = storesync.Store
type SyncKind = storesync.Kind
type SnapshotInfo = storesync.SnapshotInfo
type Noop = storesync.Noop

var ErrConflict = storesync.ErrConflict

const (
	SyncNoop   = storesync.KindNoop
	SyncAuto   = storesync.KindAuto
	SyncCAS    = storesync.KindCAS
	SyncPack   = storesync.KindPack
	SyncTar    = storesync.KindTar
	SyncRestic = storesync.KindRestic
	SyncCopy   = storesync.KindCopy
)

// Config selects a synchronization policy and configures the optional S3 remote.
type Config struct {
	ResticBinary    string
	ResticPassword  string
	Sync            string // "auto" (default) | "noop" | "cas" | "pack" | "tar" | "restic"
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
	// PersistedPaths is the BedFS durability allowlist. Empty preserves the
	// default /workspace contract for programmatic callers.
	PersistedPaths []string
}

func (c Config) remoteConfig() backend.Config {
	return backend.Config{Bucket: c.Bucket, Prefix: c.Prefix, Endpoint: c.Endpoint,
		PathStyle: c.PathStyle, Region: c.Region, AccessKeyID: c.AccessKeyID,
		SecretAccessKey: c.SecretAccessKey, SessionToken: c.SessionToken}
}
