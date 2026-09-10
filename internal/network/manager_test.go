package network

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"testing"
)

type fakeBackend struct {
	creates  int
	err      error
	endpoint *fakeEndpoint
}

func (b *fakeBackend) Create(context.Context) (endpoint, error) {
	b.creates++
	if b.err != nil {
		return nil, b.err
	}
	return b.endpoint, nil
}
func (b *fakeBackend) Close(context.Context) error { return nil }

type fakeEndpoint struct {
	closed int
	err    error
}

func (e *fakeEndpoint) Wrap(cmd *exec.Cmd)          { cmd.Args = append([]string{"network"}, cmd.Args...) }
func (e *fakeEndpoint) Gateway() string             { return "198.18.0.1" }
func (e *fakeEndpoint) Close(context.Context) error { e.closed++; return e.err }
func testManager(b backend) *Manager {
	return &Manager{report: Report{Enabled: true, Backend: "netns", Scope: "bed_processes"}, backend: b, beds: make(map[string]endpoint)}
}

func TestDisabledDoesNotChangeCommands(t *testing.T) {
	var m *Manager
	cmd := exec.Command("/bin/sh", "-c", "echo hi")
	before := cmd.String()
	if err := m.Acquire(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if err := m.Wrap("a", cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.String() != before || m.Report().Enabled {
		t.Fatal("disabled manager changed execution")
	}
}
func TestAcquireSingleFlightAndNoSilentFallback(t *testing.T) {
	b := &fakeBackend{endpoint: &fakeEndpoint{}}
	m := testManager(b)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.Acquire(context.Background(), "bed"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if b.creates != 1 {
		t.Fatalf("created %d networks", b.creates)
	}
	if err := m.Wrap("missing", exec.Command("true")); err == nil {
		t.Fatal("missing namespace silently bypassed")
	}
	if err := m.Release(context.Background(), "bed"); err != nil {
		t.Fatal(err)
	}
	if err := m.Wrap("bed", exec.Command("true")); err == nil {
		t.Fatal("released network reused")
	}
	b.err = errors.New("permission revoked")
	if err := m.Acquire(context.Background(), "bed"); !errors.Is(err, b.err) {
		t.Fatalf("runtime failure: %v", err)
	}
	if !m.Report().Enabled {
		t.Fatal("runtime failure silently disabled manager")
	}
}
func TestCleanupFailureRetainsOwnership(t *testing.T) {
	ep := &fakeEndpoint{err: errors.New("busy")}
	m := testManager(&fakeBackend{endpoint: ep})
	if err := m.Acquire(context.Background(), "bed"); err != nil {
		t.Fatal(err)
	}
	if err := m.Release(context.Background(), "bed"); err == nil {
		t.Fatal("lost cleanup error")
	}
	ep.err = nil
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ep.closed != 2 {
		t.Fatalf("cleanup not retried: %d", ep.closed)
	}
	if err := m.Acquire(context.Background(), "bed"); err == nil {
		t.Fatal("closed manager accepted network")
	}
}
