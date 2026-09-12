package resource

import (
	"context"
	"errors"
	"os"
	"sync"
)

// hostTracker keeps the injected Tracker identity stable while Start selects
// actual accounting. Executor factories can bind it before host initialization.
type hostTracker struct {
	mu              sync.RWMutex
	tracker         Tracker
	create          func() Tracker
	started, closed bool
}

func (t *hostTracker) Start(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return errors.New("resource: accounting closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !t.started {
		t.tracker = t.create()
		t.started = true
	}
	return nil
}
func (t *hostTracker) Report() Report { t.mu.RLock(); defer t.mu.RUnlock(); return t.tracker.Report() }
func (t *hostTracker) OpenGroup(id string) (*os.File, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.closed {
		return nil, os.ErrClosed
	}
	return t.tracker.OpenGroup(id)
}
func (t *hostTracker) ExecutorGroup(id, executorID string) (Group, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.closed {
		return nil, os.ErrClosed
	}
	return t.tracker.ExecutorGroup(id, executorID)
}
func (t *hostTracker) Usage(id string) (Usage, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.tracker.Usage(id)
}
func (t *hostTracker) Release(id string) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.tracker.Release(id)
}
func (t *hostTracker) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	if closer, ok := t.tracker.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}
