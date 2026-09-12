package manager

import (
	"context"
	"errors"
	"fmt"
	model "github.com/qiankunli/hostel/internal/bed"
	"log"
	"os"
	"sync"
	"time"
)

type daemonComponent struct {
	name      string
	lifecycle model.DaemonLifecycle
}

// Start initializes domain components and reserves retained identities before
// admission. Constructors and configuration never launch background loops.
func (m *Manager) Start(ctx context.Context) error {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return ErrBedUnavailable
	}
	if m.started || m.startErr != nil {
		return m.startErr
	}
	components := []daemonComponent{
		{"filesystem", m.files}, {"privilege", m.privileges}, {"resource", m.resourceManager},
		{"store", m.store}, {"network", m.network}, {"executor", m.executorManager},
	}
	for _, component := range components {
		if err := component.lifecycle.Start(ctx); err != nil {
			m.startErr = fmt.Errorf("start %s: %w", component.name, err)
			break
		}
		m.components = append(m.components, component)
	}
	if m.startErr == nil {
		m.startErr = os.MkdirAll(m.root, 0755)
		if m.startErr == nil {
			m.startErr = m.recoverLocalIdentities()
		}
	}
	if m.startErr != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		m.startErr = errors.Join(m.startErr, m.closeComponents(cleanup))
		return m.startErr
	}
	m.started = true
	return nil
}
func (m *Manager) closeComponents(ctx context.Context) error {
	var result error
	// Keep components until Close succeeds so a failed shutdown can be retried.
	for len(m.components) > 0 {
		last := m.components[len(m.components)-1]
		if err := last.lifecycle.Close(ctx); err != nil {
			return errors.Join(result, fmt.Errorf("close %s: %w", last.name, err))
		}
		m.components = m.components[:len(m.components)-1]
	}
	return result
}

// Run owns and joins every cross-Bed loop. A terminal worker error cancels its
// siblings; transient errors and backoff remain inside their domain controller.
func (m *Manager) Run(ctx context.Context) error {
	if err := m.Start(ctx); err != nil {
		return err
	}
	m.runMu.Lock()
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed || m.runDone != nil {
		m.runMu.Unlock()
		return fmt.Errorf("bed manager is closed or already running")
	}
	ctx, cancel := context.WithCancel(ctx)
	m.runCancel, m.runDone = cancel, make(chan struct{})
	done := m.runDone
	m.runMu.Unlock()
	defer close(done)
	defer cancel()
	var wg sync.WaitGroup
	failures := make(chan error, 3)
	launch := func(name string, run func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				failures <- fmt.Errorf("%s: %w", name, err)
				cancel()
			}
		}()
	}
	launch("store", m.store.Run)
	launch("resource", m.resourceManager.Run)
	launch("bed collection", m.runCollection)
	wg.Wait()
	close(failures)
	var result error
	for err := range failures {
		result = errors.Join(result, err)
	}
	return result
}
func (m *Manager) runCollection(ctx context.Context) error {
	var idle <-chan time.Time
	if m.bedIdleTTL > 0 {
		ticker := time.NewTicker(max(m.bedIdleTTL/2, time.Millisecond))
		defer ticker.Stop()
		idle = ticker.C
	}
	luggage := time.NewTicker(time.Minute)
	defer luggage.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-idle:
			if ids := m.CollectExpired(ctx, now); len(ids) > 0 {
				log.Printf("hostel: reaped idle beds: %v", ids)
			}
		case <-luggage.C:
			if ids := m.CollectLuggage(ctx); len(ids) > 0 {
				log.Printf("hostel: reaped luggage: %v", ids)
			}
		}
	}
}
func (m *Manager) stopBackground(ctx context.Context) error {
	m.startMu.Lock()
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.startMu.Unlock()
	m.runMu.Lock()
	cancel, done := m.runCancel, m.runDone
	m.runMu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var _ model.DaemonLifecycle = (*Manager)(nil)
var _ model.Runnable = (*Manager)(nil)

// Close is called after HTTP admission stops. Pending retirements retain the
// same cleanup owners and are retried alongside resident Beds.
func (m *Manager) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := m.closeMu.Acquire(ctx, 1); err != nil {
		return err
	}
	defer m.closeMu.Release(1)
	if ctx == nil {
		ctx = context.Background()
	}
	stopCleanup := context.AfterFunc(ctx, m.cleanupCancel)
	defer stopCleanup()
	if err := m.stopBackground(ctx); err != nil {
		return err
	}
	if err := m.store.StopTransfers(ctx, ""); err != nil {
		return err
	}
	if err := m.cancelAllInitializations(ctx); err != nil {
		return err
	}
	if err := m.joinPurges(ctx); err != nil {
		return err
	}
	beds := m.List()
	m.mu.Lock()
	for _, b := range m.retirements {
		beds = append(beds, b)
	}
	m.mu.Unlock()
	var closeErr error
	for _, b := range beds {
		m.mu.Lock()
		retiring := m.retirements[b.Name] == b
		m.mu.Unlock()
		if retiring {
			_, err := m.finishRetirement(ctx, b)
			closeErr = errors.Join(closeErr, err)
			continue
		}
		m.executions.killBed(b.Name, CauseDaemonShutdown)
		if err := b.cleanupMu.Acquire(ctx, 1); err != nil {
			closeErr = errors.Join(closeErr, err)
			continue
		}
		closeErr = errors.Join(closeErr, m.teardown(ctx, b))
		b.cleanupMu.Release(1)
	}
	if closeErr != nil {
		return closeErr
	}
	closeErr = errors.Join(m.RetryLocalCleanups(ctx), m.closeComponents(ctx))
	return closeErr
}
