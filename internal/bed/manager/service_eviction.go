package manager

import (
	"context"
	"errors"
	"time"

	model "github.com/qiankunli/hostel/internal/bed"
)

// Service Beds have background writers even when operation count is zero.
// Their final snapshot therefore uses a closed admission fence and quiesces
// supervision. The cleanup gate serializes this with daemon shutdown/retries.
func (m *Manager) evictServiceBed(ctx context.Context, b *Resident, cutoff *time.Time) (evicted bool, retErr error) {
	if err := b.cleanupMu.Acquire(ctx, 1); err != nil {
		return false, err
	}
	locked := true
	defer func() {
		if locked {
			b.cleanupMu.Release(1)
		}
	}()
	m.mu.Lock()
	b.mu.Lock()
	started, err := m.beginEvictionLocked(b, cutoff, true)
	if !started {
		b.mu.Unlock()
		m.mu.Unlock()
		return false, err
	}
	sequence := b.activitySeq
	b.mu.Unlock()
	m.mu.Unlock()
	trace := beginLifecycle(ctx, b.Name, lifecycleEvict)
	defer func() { trace.finish(lifecycleResult(retErr), retErr) }()
	quiesced := false
	defer func() {
		m.mu.Lock()
		resume := !m.closed && m.beds[b.Name] == b
		m.mu.Unlock()
		if !resume {
			return
		}
		if quiesced {
			recovery, cancel := context.WithTimeout(m.cleanupCtx, initializationTimeout)
			defer cancel()
			if err := m.services.Release(recovery, b.Bed); err != nil {
				retErr = errors.Join(retErr, err)
			} else {
				retErr = errors.Join(retErr, m.services.PrepareBed(recovery, b.Bed, serviceRuntime{manager: m, bed: b}))
			}
		}
		// Failed cleanup remains non-ready and retryable via another eviction;
		// never publish a stopped required service as Initialized.
		m.setLifecycle(b.Bed, model.LifecycleStatus{Phase: PhaseResident, UpdatedAt: time.Now()})
		m.servicesChanged(b.Bed)
	}()
	if err := trace.stage("stop_services", func() error { return m.services.Stop(ctx, b.Bed) }); err != nil {
		return false, err
	}
	quiesced = true
	m.revokeSessions(ctx, b)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := m.persistBed(ctx, b, "evict"); err != nil {
		return false, err
	}
	m.mu.Lock()
	b.mu.Lock()
	if m.closed || m.beds[b.Name] != b || b.inflight > 0 || b.activitySeq != sequence {
		b.mu.Unlock()
		m.mu.Unlock()
		return false, nil
	}
	m.retirements[b.Name] = b
	delete(m.beds, b.Name)
	if b.Name != m.defaultBed {
		m.residentBeds.Add(-1)
	}
	b.mu.Unlock()
	m.mu.Unlock()
	// Retirement owns the same semaphore and exact Bed allocation from here.
	locked = false
	b.cleanupMu.Release(1)
	return m.finishRetirement(ctx, b)
}
