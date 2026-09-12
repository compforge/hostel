package resource

import (
	"context"
	"errors"
	"testing"

	"github.com/qiankunli/hostel/internal/bed"
)

type partialTracker struct {
	Tracker
	starts, closes       int
	failStart, failClose error
}

func (t *partialTracker) Start(context.Context) error { t.starts++; return t.failStart }
func (t *partialTracker) Close() error                { t.closes++; return t.failClose }
func TestAccountingStartsOnlyInManagerLifecycle(t *testing.T) {
	calls := 0
	host := &partialTracker{Tracker: Noop("test")}
	tracker := &hostTracker{tracker: Noop("not started"), create: func() Tracker { calls++; return host }}
	m := NewManager(tracker, bed.NewOwners().Resource)
	if calls != 0 || m.Status().Accounting.Reason != "not started" {
		t.Fatal("constructor probed accounting")
	}
	for range 2 {
		if err := m.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("probe count=%d", calls)
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if host.closes != 1 {
		t.Fatal("host allocation not released")
	}
	if err := m.Start(t.Context()); err == nil {
		t.Fatal("closed accounting restarted")
	}
}
func TestPartialAccountingStartCanBeCleanedAndRetried(t *testing.T) {
	failure := errors.New("partial accounting setup")
	tracker := &partialTracker{Tracker: Noop("test"), failStart: failure, failClose: errors.New("busy")}
	m := NewManager(tracker, bed.NewOwners().Resource)
	if err := m.Start(t.Context()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := m.Close(t.Context()); err == nil {
		t.Fatal("cleanup failure hidden")
	}
	tracker.failClose = nil
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if tracker.starts != 1 || tracker.closes != 2 {
		t.Fatal("partial allocation cleanup not retried")
	}
}
