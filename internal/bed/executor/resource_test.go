package executor

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/resource"
)

type recordingTracker struct {
	resource.Tracker
	groups         []*recordingGroup
	parentReleases int
}
type recordingGroup struct {
	mu     sync.Mutex
	closed bool
	err    error
}

func (*recordingGroup) Open() (*os.File, error) { return nil, nil }
func (g *recordingGroup) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return g.err
	}
	g.closed = true
	return nil
}
func (t *recordingTracker) ExecutorGroup(string, string) (resource.Group, error) {
	group := &recordingGroup{}
	t.groups = append(t.groups, group)
	return group, nil
}
func (t *recordingTracker) Release(string) error { t.parentReleases++; return nil }

func TestExecutorCleanupOwnsOnlyItsAllocation(t *testing.T) {
	tracker := &recordingTracker{Tracker: resource.Noop("test")}
	factory := NewLocalFactory(tracker)
	old, err := factory.Create(context.Background(), "bed")
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	fresh, err := factory.Create(context.Background(), "bed")
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Shutdown(context.Background())
	if err := old.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tracker.parentReleases != 0 || len(tracker.groups) != 2 || tracker.groups[1].closed {
		t.Fatal("old executor released the Bed parent or new Executor group")
	}
}

func TestExecutorCleanupFailureCanBeRetried(t *testing.T) {
	tracker := &recordingTracker{Tracker: resource.Noop("test")}
	executor, err := NewLocalFactory(tracker).Create(context.Background(), "bed")
	if err != nil {
		t.Fatal(err)
	}
	group := tracker.groups[0]
	group.err = errors.New("group busy")
	if err := executor.Shutdown(context.Background()); !errors.Is(err, group.err) {
		t.Fatalf("cleanup error: %v", err)
	}
	group.err = nil
	if err := executor.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !group.closed || tracker.parentReleases != 0 {
		t.Fatal("cleanup ownership lost")
	}
}
