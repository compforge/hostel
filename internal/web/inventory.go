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

package web

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/qiankunli/hostel/internal/bed"
)

// GET /v1/inventory — the scheduler-facing snapshot: capacity plus every bed
// this instance holds (active/idle/evicting in memory, luggage on disk) with its
// last persisted generation. Everything here is a stale-tolerant hint —
// freshness is re-enforced at activation, so routing on outdated inventory
// is slow, never wrong. Callers must treat store "noop" as "beds are pinned
// here": no snapshot exists elsewhere to migrate from.
func (s *Server) inventory(c *gin.Context) {
	beds := s.mgr.Inventory()
	hasBeds := false
	counts := map[string]int{
		string(bed.StateActive):   0,
		string(bed.StateIdle):     0,
		string(bed.StateEvicting): 0,
		string(bed.StateDormant):  0,
	}
	var luggageBytes int64
	var retainUntil time.Time
	expiryKnown := true
	for _, b := range beds {
		counts[string(b.State)]++
		if b.State == bed.StateDormant {
			luggageBytes += b.Bytes
		} else {
			hasBeds = true
			if b.RetainUntil.IsZero() {
				expiryKnown = false
			} else if b.RetainUntil.After(retainUntil) {
				retainUntil = b.RetainUntil
			}
		}
	}
	high, low := s.mgr.LuggageLimits()
	var instanceRetainUntil any
	if hasBeds && expiryKnown {
		instanceRetainUntil = retainUntil
	}
	c.JSON(http.StatusOK, gin.H{
		"instance": gin.H{
			"store":              s.mgr.StoreName(),
			"isolation":          s.mgr.Isolator().Level().String(),
			"max_beds":           s.mgr.MaxBeds(),
			"bed_counts":         counts,
			"retained_until":     instanceRetainUntil,
			"luggage_bytes":      luggageBytes,
			"luggage_high_bytes": high,
			"luggage_low_bytes":  low,
		},
		"beds": beds,
	})
}
