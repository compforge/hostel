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
	"context"
	"log"
	"time"

	"github.com/qiankunli/go-stdx/filepathx"
)

const (
	storeSyncRetryMin = time.Second
	storeSyncRetryMax = 30 * time.Second
)

// RequestStoreSync submits a coalesced urgency signal. Callers never upload
// directly: the store loop owns serialization, periodic cadence and retries.
func (m *Manager) RequestStoreSync() {
	select {
	case m.storeSync <- struct{}{}:
	default:
	}
}

// RunStoreSync runs the persistence controller until ctx is canceled. A zero
// interval disables only the periodic safety net; lifecycle and pressure
// triggers still wake the controller.
func (m *Manager) RunStoreSync(ctx context.Context, interval time.Duration) {
	var periodic <-chan time.Time
	var ticker *time.Ticker
	if interval > 0 {
		ticker = time.NewTicker(interval)
		periodic = ticker.C
		defer ticker.Stop()
	}

	var retry <-chan time.Time
	var retryTimer *time.Timer
	retryDelay := storeSyncRetryMin
	stopRetry := func() {
		if retryTimer != nil && !retryTimer.Stop() {
			select {
			case <-retryTimer.C:
			default:
			}
		}
		retryTimer = nil
		retry = nil
		retryDelay = storeSyncRetryMin
	}
	defer stopRetry()

	run := func(trigger string) {
		if m.store.Name() == "noop" {
			m.sampleLocalBytes()
			return
		}
		done, failed := m.persistDirty(ctx, trigger)
		if len(done) > 0 {
			log.Printf("hostel: store synced beds: %v", done)
		}
		if !failed {
			stopRetry()
			return
		}
		if retryTimer == nil {
			retryTimer = time.NewTimer(retryDelay)
			retry = retryTimer.C
			if retryDelay < storeSyncRetryMax {
				retryDelay *= 2
				if retryDelay > storeSyncRetryMax {
					retryDelay = storeSyncRetryMax
				}
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.storeSync:
			run("requested")
		case <-periodic:
			run("periodic")
		case <-retry:
			retryTimer = nil
			retry = nil
			run("retry")
		}
	}
}

// sampleLocalBytes keeps directory scans out of GET /v1/beds. Lifecycle and
// pressure triggers only wake this serialized controller; the controller owns
// when the stale-tolerant size hint is refreshed.
func (m *Manager) sampleLocalBytes() {
	for _, b := range m.List() {
		bytes := filepathx.DirBytes(b.Dir)
		b.mu.Lock()
		b.localBytes = bytes
		b.mu.Unlock()
	}
}
