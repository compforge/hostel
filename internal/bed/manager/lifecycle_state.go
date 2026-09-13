package manager

import (
	"time"

	model "github.com/qiankunli/hostel/internal/bed"
)

// setLifecycle changes phase/readiness without erasing the independent
// retention clock. Keepalive has one writer path and survives cleanup retries
// and service readiness changes; it is not reconstructed from LastActiveAt.
func (m *Manager) setLifecycle(b *model.Bed, next model.LifecycleStatus) {
	m.owners.Lifecycle.Update(b, func(current *model.LifecycleStatus) {
		next.KeepaliveAt = current.KeepaliveAt
		*current = next
	})
}

// keepaliveLocked is shared by activity and explicit renewal. Callers serialize
// it with admission/eviction using m.mu then b.mu (or own unpublished initialization).
// +spec=`Keepalive never moves backwards and does not imply activity or a data mutation.`
func (m *Manager) keepaliveLocked(b *managedBed, base time.Time) {
	m.owners.Lifecycle.Update(b.Bed, func(s *model.LifecycleStatus) {
		if base.After(s.KeepaliveAt) {
			s.KeepaliveAt = base.UTC()
		}
	})
}

func retentionDeadline(base time.Time, ttl time.Duration) time.Time {
	if ttl <= 0 || base.IsZero() {
		return time.Time{}
	}
	return base.Add(ttl)
}

func (b *managedBed) retainedUntilLocked() time.Time {
	return retentionDeadline(b.Bed.Status().Lifecycle.KeepaliveAt, b.idleTTL)
}

// beginEvictionLocked is the common admission fence for automatic and explicit
// eviction. Callers hold m.mu then b.mu; no resource I/O runs under these locks.
// +spec=`Renewal that wins the eviction fence postpones idle GC; renewal after the fence cannot resurrect a Bed.`
func (m *Manager) beginEvictionLocked(b *managedBed, cutoff *time.Time, stopServices bool) (bool, error) {
	if m.closed || m.beds[b.Name] != b || b.purging {
		return false, ErrBedUnavailable
	}
	if b.Bed.Status().Lifecycle.Phase != PhaseResident || b.inflight > 0 {
		return false, nil
	}
	until := b.retainedUntilLocked()
	if cutoff != nil && (until.IsZero() || until.After(*cutoff)) {
		return false, nil
	}
	status := model.LifecycleStatus{Phase: PhaseEvicting, Ready: !stopServices, Reason: "Evicting", UpdatedAt: time.Now()}
	if stopServices {
		status.Reason = "StoppingServices"
	}
	m.setLifecycle(b.Bed, status)
	return true, nil
}
