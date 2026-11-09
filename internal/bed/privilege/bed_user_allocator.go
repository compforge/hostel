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

// BedUserLease is an exact allocation. An old handle cannot release a later
// allocation, even when the Bed ID and UID are reused.
type BedUserLease struct {
	user      BedUser
	allocator *BedUserAllocator
	bedID     string
}

func (l *BedUserLease) UID() int { return l.user.UID() }
func (l *BedUserLease) GID() int { return l.user.GID() }

func (l *BedUserLease) Release() {
	a := l.allocator
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.byBed[l.bedID] != l {
		return
	}
	delete(a.byBed, l.bedID)
	if a.fixed == nil {
		delete(a.ownerUID, l.UID())
		delete(a.ownerGID, l.GID())
	}
}

// BedUserAllocator owns local Unix identity leases. Fixed strategy deliberately
// shares a user; per-Bed strategy reserves each UID and primary GID for one Bed ID.
type BedUserAllocator struct {
	mu             sync.Mutex
	fixed          *BedUser
	uidMin, uidMax int
	byBed          map[string]*BedUserLease
	ownerUID       map[int]string
	ownerGID       map[int]string
	pending        map[string]identityReservation
}

type identityReservation struct{ uid, gid *int }

func NewFixedBedUserAllocator(user BedUser) *BedUserAllocator {
	return &BedUserAllocator{fixed: &user, byBed: make(map[string]*BedUserLease), pending: make(map[string]identityReservation)}
}
func NewPerBedUserAllocator(uidMin, uidMax int) (*BedUserAllocator, error) {
	if uidMin <= 0 || uidMax < uidMin {
		return nil, fmt.Errorf("privilege: invalid bed user range %d..%d", uidMin, uidMax)
	}
	return &BedUserAllocator{uidMin: uidMin, uidMax: uidMax,
		byBed: make(map[string]*BedUserLease), ownerUID: make(map[int]string), ownerGID: make(map[int]string), pending: make(map[string]identityReservation)}, nil
}
func (a *BedUserAllocator) allocateLocked(bedID string, user BedUser) *BedUserLease {
	lease := &BedUserLease{user: user, allocator: a, bedID: bedID}
	a.byBed[bedID] = lease
	delete(a.pending, bedID)
	if a.fixed == nil {
		a.ownerUID[user.UID()] = bedID
		a.ownerGID[user.GID()] = bedID
	}
	return lease
}

// Restore reserves only recorded fields. Call it for every recovered Bed before
// Acquire fills missing fields, so allocation cannot steal another record's GID.
// +spec=`UID and primary GID are independently reused when present and allocated when absent; dedicated identities never share either value across Beds.`
// +link:doc=docs/bed.md
func (a *BedUserAllocator) Restore(bedID string, uid, gid *int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	prior := a.pending[bedID]
	if lease := a.byBed[bedID]; lease != nil {
		u, g := lease.UID(), lease.GID()
		prior = identityReservation{uid: &u, gid: &g}
	}
	validate := func(kind string, value, previous *int, owners map[int]string, fixed int) error {
		if value == nil {
			return nil
		}
		if previous != nil && *previous != *value {
			return fmt.Errorf("privilege: bed %s has conflicting %s values %d and %d", bedID, kind, *previous, *value)
		}
		if a.fixed != nil {
			if *value != fixed {
				return fmt.Errorf("privilege: bed %s recorded %s %d conflicts with shared identity %d", bedID, kind, *value, fixed)
			}
		} else {
			if *value < a.uidMin || *value > a.uidMax {
				return fmt.Errorf("privilege: bed %s %s %d is outside per-bed range %d..%d", bedID, kind, *value, a.uidMin, a.uidMax)
			}
			if owner, occupied := owners[*value]; occupied && owner != bedID {
				return fmt.Errorf("privilege: %s %d is owned by both %s and %s", kind, *value, owner, bedID)
			}
		}
		return nil
	}
	var fixedUID, fixedGID int
	if a.fixed != nil {
		fixedUID, fixedGID = a.fixed.UID(), a.fixed.GID()
	}
	if err := validate("uid", uid, prior.uid, a.ownerUID, fixedUID); err != nil {
		return err
	}
	if err := validate("gid", gid, prior.gid, a.ownerGID, fixedGID); err != nil {
		return err
	}
	if a.byBed[bedID] != nil {
		return nil
	}
	if uid != nil {
		value := *uid
		prior.uid = &value
	}
	if gid != nil {
		value := *gid
		prior.gid = &value
	}
	a.pending[bedID] = prior
	if a.fixed == nil {
		if prior.uid != nil {
			a.ownerUID[*prior.uid] = bedID
		}
		if prior.gid != nil {
			a.ownerGID[*prior.gid] = bedID
		}
	}
	return nil
}

func (a *BedUserAllocator) Acquire(bedID string) (*BedUserLease, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if lease := a.byBed[bedID]; lease != nil {
		return lease, nil
	}
	if a.fixed != nil {
		return a.allocateLocked(bedID, *a.fixed), nil
	}
	reservation := a.pending[bedID]
	slots := a.uidMax - a.uidMin + 1
	preferred := a.uidMin + stableBedUserSlot(bedID, slots)
	if reservation.gid != nil {
		preferred = *reservation.gid
	}
	uid, err := a.selectIDLocked(bedID, reservation.uid, preferred, a.ownerUID)
	if err != nil {
		return nil, fmt.Errorf("allocate uid: %w", err)
	}
	gid, err := a.selectIDLocked(bedID, reservation.gid, uid, a.ownerGID)
	if err != nil {
		return nil, fmt.Errorf("allocate gid: %w", err)
	}
	user, err := NewBedUser(uid, gid)
	if err != nil {
		return nil, err
	}
	return a.allocateLocked(bedID, user), nil
}

func (a *BedUserAllocator) selectIDLocked(bedID string, recorded *int, preferred int, owners map[int]string) (int, error) {
	if recorded != nil {
		return *recorded, nil
	}
	slots := a.uidMax - a.uidMin + 1
	for offset := 0; offset < slots; offset++ {
		value := a.uidMin + (preferred-a.uidMin+offset)%slots
		if owner, occupied := owners[value]; !occupied || owner == bedID {
			return value, nil
		}
	}
	return 0, fmt.Errorf("privilege: bed %s identity range %d..%d exhausted", bedID, a.uidMin, a.uidMax)
}

func stableBedUserSlot(bedID string, slots int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(bedID))
	return int(h.Sum32() % uint32(slots))
}
