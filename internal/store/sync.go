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
	"log"
	"time"
)

// SyncKind selects how files are synchronized and organized remotely.
// Bed Sync controls automatic persistence; Transfer Sync selects one explicit operation.
type SyncKind string

const (
	SyncNoop   SyncKind = "noop"
	SyncAuto   SyncKind = "auto"
	SyncCAS    SyncKind = "cas"
	SyncPack   SyncKind = "pack"
	SyncTar    SyncKind = "tar"
	SyncRestic SyncKind = "restic"
	SyncCopy   SyncKind = "copy" // Explicit file transfers only.
)

const (
	storeSyncRetryMin = time.Second
	storeSyncRetryMax = 30 * time.Second
)

// RequestSync submits a coalesced urgency signal. Callers never upload
// directly: the store loop owns serialization, periodic cadence and retries.
func (s *Manager) RequestSync() {
	select {
	case s.syncRequested <- struct{}{}:
	default:
	}
}

// Run runs the persistence controller until ctx is canceled. A zero
// interval disables only the periodic safety net; lifecycle and pressure
// triggers still wake the controller.
// syncBeds supplies a lifecycle-safe pass over the current Bed list. The Bed
// manager retains its lock and watermark protocol; Manager owns invocation cadence.
func (s *Manager) Run(ctx context.Context, interval time.Duration, syncBeds func(context.Context, string) ([]string, bool)) {
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
		done, failed := syncBeds(ctx, trigger)
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
		case <-s.syncRequested:
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
