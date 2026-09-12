package resource

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/qiankunli/hostel/internal/feature"
)

// hostTracker keeps the injected Tracker identity stable while Start selects
// actual accounting. Executor factories can bind it before host initialization.
type hostTracker struct {
	config          Config
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
	if err := t.config.Validate(); err != nil {
		return fmt.Errorf("resource: %w", err)
	}
	if !t.started {
		if t.config.Cgroup.Effective() == feature.Off {
			t.tracker = Noop("disabled_by_config")
		} else {
			t.tracker = t.create()
		}
		t.started = true
	}
	report := t.report()
	log.Printf("resource: feature=cgroup policy=%s selected=%t reason=%s", t.config.Cgroup.Effective(), report.Available, report.Reason)
	return report.Features["cgroup"].CheckRequired("resource.cgroup")
}
func (t *hostTracker) Report() Report { t.mu.RLock(); defer t.mu.RUnlock(); return t.report() }
func (t *hostTracker) report() Report {
	report := t.tracker.Report()
	requirements := feature.Requirements{Conditions: []string{"Linux cgroup v2", "writable delegated hierarchy", "cpu/memory controller delegation"}}
	report.Features = map[string]feature.Status{"cgroup": feature.Describe(t.config.Cgroup, requirements, t.started, report.Available, report.Available, report.Reason)}
	return report
}
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
