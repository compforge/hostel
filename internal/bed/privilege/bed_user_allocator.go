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

package privilege

import (
	"fmt"
	"hash/fnv"
	"sync"
)

// BedUserAllocator owns the one-to-one mapping between live Bed identities and
// Unix users. Per-Bed allocation starts at a deterministic slot, then probes
// the configured range so hash collisions never weaken isolation silently.
type BedUserAllocator struct {
	mu       sync.Mutex
	fixed    *BedUser
	uidMin   int
	uidMax   int
	byBed    map[string]BedUser
	ownerUID map[int]string
}

func NewFixedBedUserAllocator(user BedUser) *BedUserAllocator {
	return &BedUserAllocator{fixed: &user}
}

func NewPerBedUserAllocator(uidMin, uidMax int) (*BedUserAllocator, error) {
	if uidMin <= 0 || uidMax < uidMin {
		return nil, fmt.Errorf("privilege: invalid bed user range %d..%d", uidMin, uidMax)
	}
	return &BedUserAllocator{
		uidMin: uidMin, uidMax: uidMax,
		byBed: make(map[string]BedUser), ownerUID: make(map[int]string),
	}, nil
}

func (a *BedUserAllocator) reserveLocked(bedID string, uid int) error {
	if a.fixed != nil {
		return nil
	}
	if uid < a.uidMin || uid > a.uidMax {
		return nil
	}
	if user, ok := a.byBed[bedID]; ok {
		if user.UID() != uid {
			return fmt.Errorf("privilege: bed %s has conflicting uids %d and %d", bedID, user.UID(), uid)
		}
		return nil
	}
	if owner, occupied := a.ownerUID[uid]; occupied && owner != bedID {
		return fmt.Errorf("privilege: uid %d is owned by both %s and %s", uid, owner, bedID)
	}
	user, err := NewBedUser(uid, uid)
	if err != nil {
		return err
	}
	a.byBed[bedID] = user
	a.ownerUID[uid] = bedID
	return nil
}

func (a *BedUserAllocator) Acquire(bedID string) (BedUser, error) {
	if a == nil {
		return BedUser{}, fmt.Errorf("privilege: bed user allocator is not configured")
	}
	if a.fixed != nil {
		return *a.fixed, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if user, ok := a.byBed[bedID]; ok {
		return user, nil
	}
	slots := a.uidMax - a.uidMin + 1
	start := stableBedUserSlot(bedID, slots)
	for offset := 0; offset < slots; offset++ {
		uid := a.uidMin + (start+offset)%slots
		if _, occupied := a.ownerUID[uid]; occupied {
			continue
		}
		user, err := NewBedUser(uid, uid)
		if err != nil {
			return BedUser{}, err
		}
		a.byBed[bedID] = user
		a.ownerUID[uid] = bedID
		return user, nil
	}
	return BedUser{}, fmt.Errorf("privilege: bed user range %d..%d exhausted", a.uidMin, a.uidMax)
}

// Release frees a mapping only after the Bed's local tree has been removed.
// Calling it for a fixed strategy is harmless.
func (a *BedUserAllocator) Release(bedID string) {
	if a == nil || a.fixed != nil {
		return
	}
	a.mu.Lock()
	if user, ok := a.byBed[bedID]; ok {
		delete(a.ownerUID, user.UID())
		delete(a.byBed, bedID)
	}
	a.mu.Unlock()
}

func stableBedUserSlot(bedID string, slots int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(bedID))
	return int(h.Sum32() % uint32(slots))
}
