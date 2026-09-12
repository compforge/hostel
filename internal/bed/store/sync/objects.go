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
	"io"

	"github.com/qiankunli/hostel/internal/bed/store/backend"
)

const objectOpTimeout = backend.OperationTimeout

// generationMetaKey belongs to snapshot layouts, not to the storage backend.
const generationMetaKey = "generation"

// objects is the object-storage contract consumed by snapshot policies.
// Layouts depend on these operations rather than an SDK client or Manager.
type objects interface {
	// Head returns object user metadata and size; exists=false when missing.
	Head(ctx context.Context, key string) (meta map[string]string, size int64, exists bool, err error)
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// PutObject may rewind the body for signing, checksums, and retries. Hostel's
	// chunks, packs and indexes are bounded in memory, while full tar snapshots
	// use a temporary file. Require a seekable body instead of weakening S3
	// integrity for non-seekable streams.
	Put(ctx context.Context, key string, r io.ReadSeeker, size int64, meta map[string]string) error
	// Delete removes keys; missing keys are not an error.
	Delete(ctx context.Context, keys []string) error
	// List returns all keys under prefix.
	List(ctx context.Context, prefix string) ([]string, error)
}
