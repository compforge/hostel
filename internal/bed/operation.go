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

package bed

import (
	"sync"
	"time"
)

// OperationKind classifies stateless bed operations (docs/lifecycle.md).
type OperationKind string

const (
	OpExec       OperationKind = "exec"
	OpFile       OperationKind = "file"
	OpBrowser    OperationKind = "browser"
	OpCheckpoint OperationKind = "checkpoint"
	OpControl    OperationKind = "control"
)

// Operation timeout policy: every operation is bounded so eviction's
// refuse-and-retry always converges (docs/lifecycle.md). A non-positive
// timeout gets the default; beyond the max is clamped, not rejected.
const (
	DefaultOperationTimeout = 5 * time.Minute
	MaxOperationTimeout     = 2 * time.Hour
)

// touchBed records activity that belongs to an already-admitted operation or
// session. Keeping the compound pinned counter under the manager lock avoids
// a dirty idle bed appearing movable between requests.
func (m *Manager) touchBed(b *Bed) {
	m.mu.Lock()
	b.mu.Lock()
	if current, ok := m.beds[b.ID]; ok && current == b {
		wasPinned := b.pinnedLocked()
		b.touchLocked(time.Now(), m.bedIdleTTL)
		m.adjustPinnedLocked(b, wasPinned)
	}
	b.mu.Unlock()
	m.mu.Unlock()
	m.RequestStoreSync()
}

// BeginOperation marks a bed active and reserves it through timeout plus the
// configured idle TTL. Admission and final eviction both take m.mu then b.mu,
// so an operation cannot start on a Bed after its resident entry is removed.
// Call the returned function exactly once.
func (m *Manager) BeginOperation(b *Bed, kind OperationKind, timeout time.Duration) (func(), error) {
	if timeout <= 0 {
		timeout = DefaultOperationTimeout
	}
	if timeout > MaxOperationTimeout {
		timeout = MaxOperationTimeout
	}
	now := time.Now()
	retainUntil := now.Add(m.bedIdleTTL).Add(timeout)

	m.mu.Lock()
	b.mu.Lock()
	if current, ok := m.beds[b.ID]; !ok || current != b {
		b.mu.Unlock()
		m.mu.Unlock()
		return nil, ErrBedUnavailable
	}
	wasPinned := b.pinnedLocked()
	if !wasPinned && b.ID != m.defaultBed {
		if err := m.resourceAdmissionErrorLocked(); err != nil {
			b.mu.Unlock()
			m.mu.Unlock()
			return nil, err
		}
	}
	b.lastActiveAt = now
	b.activitySeq++
	b.inflight++
	b.inflightByKind[kind]++
	m.adjustPinnedLocked(b, wasPinned)
	if m.bedIdleTTL > 0 && retainUntil.After(b.retainUntil) {
		b.retainUntil = retainUntil
	}
	b.mu.Unlock()
	m.mu.Unlock()
	m.RequestStoreSync()

	var once sync.Once
	return func() {
		once.Do(func() {
			now := time.Now()
			m.mu.Lock()
			b.mu.Lock()
			wasPinned := b.pinnedLocked()
			if b.inflight > 0 {
				b.inflight--
			}
			if n := b.inflightByKind[kind]; n > 1 {
				b.inflightByKind[kind] = n - 1
			} else {
				delete(b.inflightByKind, kind)
			}
			// Checkpoint already captured the activity watermark set at start.
			// Moving it again after the upload would immediately make the same
			// snapshot look stale.
			if kind != OpCheckpoint {
				b.lastActiveAt = now
				b.activitySeq++
			}
			if m.bedIdleTTL > 0 {
				retainUntil := now.Add(m.bedIdleTTL)
				if retainUntil.After(b.retainUntil) {
					b.retainUntil = retainUntil
				}
			}
			if current, ok := m.beds[b.ID]; ok && current == b {
				m.adjustPinnedLocked(b, wasPinned)
			}
			b.mu.Unlock()
			m.mu.Unlock()
			m.RequestStoreSync()
		})
	}, nil
}
