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
	"fmt"
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
	becomingActive := b.inflight == 0 && b.ID != m.defaultBed
	if becomingActive && m.maxActiveBeds > 0 && m.activeBeds.Load() >= int64(m.maxActiveBeds) {
		pressure := &BedPressureError{
			ActiveBeds:    m.activeBeds.Load(),
			MaxActiveBeds: m.maxActiveBeds,
			ResidentBeds:  m.tenantResidentBedsLocked(),
			MaxBeds:       m.maxBeds,
		}
		b.mu.Unlock()
		m.mu.Unlock()
		return nil, pressure
	}
	if becomingActive {
		if decision := m.admission.Check(); !decision.Allowed {
			b.mu.Unlock()
			m.mu.Unlock()
			return nil, fmt.Errorf("%w: %s", ErrResourcePressure, decision.Reason)
		}
	}
	b.lastActiveAt = now
	b.activitySeq++
	b.inflight++
	b.inflightByKind[kind]++
	if becomingActive {
		m.activeBeds.Add(1)
	}
	if m.bedIdleTTL > 0 && retainUntil.After(b.retainUntil) {
		b.retainUntil = retainUntil
	}
	b.mu.Unlock()
	m.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			now := time.Now()
			m.mu.Lock()
			b.mu.Lock()
			if b.inflight > 0 {
				b.inflight--
				if b.inflight == 0 && b.ID != m.defaultBed {
					if current, ok := m.beds[b.ID]; ok && current == b {
						m.activeBeds.Add(-1)
					}
				}
			}
			if n := b.inflightByKind[kind]; n > 1 {
				b.inflightByKind[kind] = n - 1
			} else {
				delete(b.inflightByKind, kind)
			}
			b.lastActiveAt = now
			b.activitySeq++
			if m.bedIdleTTL > 0 {
				retainUntil := now.Add(m.bedIdleTTL)
				if retainUntil.After(b.retainUntil) {
					b.retainUntil = retainUntil
				}
			}
			b.mu.Unlock()
			m.mu.Unlock()
		})
	}, nil
}
