package service

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/executor"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

type fakeRuntime struct {
	mu        sync.Mutex
	processes []*fakeProcess
	fail      bool
	scope     string
	host      string
	start     func(Launch) (Process, error)
}

func (f *fakeRuntime) Network() (string, string, error) {
	host := f.host
	if host == "" {
		host = "127.0.0.1"
	}
	return f.scope, host, nil
}
func (f *fakeRuntime) Start(_ context.Context, launch Launch) (Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, fmt.Errorf("test start failure")
	}
	if f.start != nil {
		p, err := f.start(launch)
		if err == nil {
			if fake, ok := p.(*fakeProcess); ok {
				f.processes = append(f.processes, fake)
			}
		}
		return p, err
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
	stop    func()
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
	if p.stop != nil {
		p.stop()
	}
	p.exit(executor.Exited(0))
	return nil
}

func testController(t *testing.T, required bool) (*Manager, *bed.Bed) {
	t.Helper()
	m := NewManager(nil, "", nil)
	b := bed.New("test", "", bed.Spec{Services: []bed.ServiceSpec{{Name: "worker", Command: []string{"worker"}, Required: required, MaxRestarts: 1}}})
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

func TestBedScopedHTTPServiceUsesNetworkScopeOwnership(t *testing.T) {
	ports, err := hostnetwork.NewPortManager(25101, 25200)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(ports, "127.0.0.1", nil)
	t.Cleanup(func() {
		if err := m.Close(context.Background()); err != nil {
			t.Error(err)
		}
		if err := ports.Close(); err != nil {
			t.Error(err)
		}
	})
	runtime := &fakeRuntime{scope: "bed-network", start: func(launch Launch) (Process, error) {
		listener, err := net.Listen("tcp", launch.Env["SERVICE_LISTEN"])
		if err != nil {
			return nil, err
		}
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			if request.Header.Get("Authorization") != "Bearer "+launch.Env["SERVICE_TOKEN"] {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		})}
		go func() { _ = server.Serve(listener) }()
		process := &fakeProcess{id: "scoped", done: make(chan struct{})}
		process.stop = func() { _ = server.Close() }
		return process, nil
	}}
	b := bed.New("scoped", "", bed.Spec{Services: []bed.ServiceSpec{{
		Name: "worker", Command: []string{"worker"}, Env: map[string]string{"SERVICE_LISTEN": "${LISTEN_ADDR}"},
		Required: true, MaxRestarts: 1, StartupSeconds: 2, StopSeconds: 1,
		HTTP: &bed.ServiceHTTPSpec{ReadyPath: "/ready", TokenEnv: "SERVICE_TOKEN"},
	}}})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := m.PrepareBed(ctx, b, runtime); err != nil {
		t.Fatal(err)
	}
	status := m.Status(b)[0]
	if status.Phase != "ready" || status.Restarts != 0 || status.Endpoint == "" {
		t.Fatalf("scoped service status = %+v", status)
	}
	// The steady-state probe uses the same scope-aware ownership rule.
	time.Sleep(1100 * time.Millisecond)
	current := m.Status(b)[0]
	if current.Phase != "ready" || current.ExecutionID != status.ExecutionID {
		t.Fatalf("scoped service lost readiness: before=%+v after=%+v", status, current)
	}
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
