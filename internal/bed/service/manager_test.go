package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/executor"
)

type fakeRuntime struct {
	mu        sync.Mutex
	processes []*fakeProcess
	fail      bool
}

func (f *fakeRuntime) Network() (string, string, error) { return "", "127.0.0.1", nil }
func (f *fakeRuntime) Start(_ context.Context, _ Launch) (Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, fmt.Errorf("test start failure")
	}
	p := &fakeProcess{id: fmt.Sprint(len(f.processes) + 1), done: make(chan struct{})}
	f.processes = append(f.processes, p)
	return p, nil
}

type fakeProcess struct {
	mu      sync.Mutex
	id      string
	done    chan struct{}
	outcome executor.ProcessOutcome
	once    sync.Once
}

func (p *fakeProcess) PID() int              { return 1 }
func (p *fakeProcess) ExecutionID() string   { return p.id }
func (p *fakeProcess) ExecutorID() string    { return "executor-" + p.id }
func (p *fakeProcess) Done() <-chan struct{} { return p.done }
func (p *fakeProcess) Outcome() executor.ProcessOutcome {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.outcome
}
func (p *fakeProcess) exit(outcome executor.ProcessOutcome) {
	p.once.Do(func() { p.mu.Lock(); p.outcome = outcome; p.mu.Unlock(); close(p.done) })
}
func (p *fakeProcess) Stop(context.Context, time.Duration) error {
	p.exit(executor.Exited(0))
	return nil
}

func testController(t *testing.T, required bool) (*Manager, *bed.Bed) {
	t.Helper()
	c, err := NewCatalog([]Template{{Name: "worker", Command: []string{"worker"}, Required: required, MaxRestarts: 1}})
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(c, nil, "", nil)
	b := bed.New("test", "", bed.Spec{Services: []bed.ServiceSpec{{Name: "worker", Template: "worker"}}})
	t.Cleanup(func() {
		if err := m.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return m, b
}

func TestRequiredAndOptionalStartupFailure(t *testing.T) {
	for _, required := range []bool{false, true} {
		t.Run(fmt.Sprint(required), func(t *testing.T) {
			m, b := testController(t, required)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			err := m.PrepareBed(ctx, b, &fakeRuntime{fail: true})
			if required != (err != nil) {
				t.Fatalf("required=%t, result=%v", required, err)
			}
			if required && m.Ready(b) {
				t.Fatal("failed required service published ready")
			}
		})
	}
}

func TestExecutorLossRecoversDesiredServiceEvenWithNeverRestart(t *testing.T) {
	m, b := testController(t, true)
	runtime := &fakeRuntime{}
	if err := m.PrepareBed(t.Context(), b, runtime); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	first := runtime.processes[0]
	runtime.mu.Unlock()
	first.exit(executor.Lost("first", fmt.Errorf("test executor loss")))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status := m.Status(b)
		if status[0].Phase == "ready" && status[0].ExecutionID != first.id {
			if status[0].ExecutorID == first.ExecutorID() || status[0].Restarts != 1 {
				t.Fatalf("bad recovered ownership: %+v", status)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("service not recovered: %+v", m.Status(b))
}

func TestReleaseAndPrepareKeepsNewServiceGroup(t *testing.T) {
	m, b := testController(t, true)
	if err := m.PrepareBed(t.Context(), b, &fakeRuntime{}); err != nil {
		t.Fatal(err)
	}
	old := m.group(b)
	if err := m.Release(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if err := m.PrepareBed(t.Context(), b, &fakeRuntime{}); err != nil {
		t.Fatal(err)
	}
	if err := stopGroup(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	if !m.Ready(b) || m.group(b) == old {
		t.Fatal("old cleanup changed replacement")
	}
}
