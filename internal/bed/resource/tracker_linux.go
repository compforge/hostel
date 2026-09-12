//go:build linux

package resource

import (
	"os"
	"sync"

	"github.com/qiankunli/hostel/internal/host/cgroup"
)

const (
	cgroupMount    = "/sys/fs/cgroup"
	systemGroup    = "hostel-system"
	bedGroupPrefix = "hostel-bed-"
)

type cgroupTracker struct {
	mu   sync.Mutex
	root *cgroup.Group
	beds map[string]*cgroup.Group
}

func newTracker() Tracker {
	root, err := cgroup.Setup(cgroupMount, "/proc/self/cgroup", systemGroup)
	if err != nil {
		return Noop(err.Error())
	}
	return &cgroupTracker{root: root, beds: make(map[string]*cgroup.Group)}
}
func (t *cgroupTracker) Report() Report { return Report{Backend: "cgroupv2", Available: true} }
func (t *cgroupTracker) parent(id string) (*cgroup.Group, error) {
	if g := t.beds[id]; g != nil {
		return g, nil
	}
	g, err := t.root.OpenChild(bedGroupPrefix + id)
	if err == nil {
		t.beds[id] = g
	}
	return g, err
}
func (t *cgroupTracker) OpenGroup(id string) (*os.File, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	g, err := t.parent(id)
	if err != nil {
		return nil, err
	}
	return g.Open()
}

// Only Executor children receive processes: the Bed parent stays empty for
// delegation and accumulates usage across Executor replacements.
func (t *cgroupTracker) ExecutorGroup(id, executorID string) (Group, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	g, err := t.parent(id)
	if err != nil {
		return nil, err
	}
	if err := g.EnableControllers("cpu", "memory"); err != nil {
		return nil, err
	}
	return g.CreateChild(executorID)
}
func (t *cgroupTracker) Usage(id string) (Usage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	g := t.beds[id]
	if g == nil {
		return Usage{}, nil
	}
	u, err := g.Usage()
	return Usage{CPUUsage: u.CPUUsage, MemoryCurrentBytes: u.MemoryCurrentBytes}, err
}
func (t *cgroupTracker) Release(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	g := t.beds[id]
	if g == nil {
		return nil
	}
	if err := g.Close(); err != nil {
		return err
	}
	delete(t.beds, id)
	return nil
}

func (t *cgroupTracker) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.root.Close()
}
