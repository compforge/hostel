package manager

import (
	"errors"
	"log"
	"time"
)

var ErrInvalidExpiration = errors.New("bed: expiresAt must be a future timestamp")

// RenewExpiration extends an existing resident Bed's idle-retention deadline.
// It does not touch activity, create an operation, or prevent explicit eviction.
// Nil expiresAt refreshes one idle TTL from now; a non-nil timestamp sets a
// minimum deadline. A zero result means automatic expiration is disabled.
func (m *Manager) RenewExpiration(b *Resident, expiresAt *time.Time) (time.Time, error) {
	if b == nil {
		return time.Time{}, ErrBedUnavailable
	}
	// The same lock order as operation admission and eviction commits fences
	// stale handles. Once eviction starts, renewal cannot resurrect the Bed.
	m.mu.Lock()
	b.mu.Lock()
	if m.closed || m.beds[b.Name] != b || b.purging || b.Bed.Status().Lifecycle.Phase != PhaseResident {
		b.mu.Unlock()
		m.mu.Unlock()
		return time.Time{}, ErrBedUnavailable
	}
	now := time.Now()
	if expiresAt != nil && (!expiresAt.After(now) || expiresAt.Year() > 9999) {
		b.mu.Unlock()
		m.mu.Unlock()
		return time.Time{}, ErrInvalidExpiration
	}
	previous := b.retainedUntilLocked()
	base := now
	// Zero TTL disables idle GC. Explicit expiration cannot turn it back on.
	if expiresAt != nil && b.idleTTL > 0 {
		base = expiresAt.Add(-b.idleTTL)
	}
	m.keepaliveLocked(b, base)
	effective := b.retainedUntilLocked()
	b.mu.Unlock()
	m.mu.Unlock()
	if !effective.Equal(previous) {
		log.Printf("hostel: bed retention extended: bed=%s retained_until=%s", b.Short(), effective.Format(time.RFC3339Nano))
	}
	return effective, nil
}
