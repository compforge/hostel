package service

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/executor"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

func httpTestManager(t *testing.T) (*Manager, *hostnetwork.PortManager) {
	t.Helper()
	ports, err := hostnetwork.NewPortManager(25301, 25400)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(ports, "127.0.0.1", nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
		if len(ports.Status()) != 0 {
			t.Errorf("leaked allocations: %+v", ports.Status())
		}
		if err := ports.Close(); err != nil {
			t.Error(err)
		}
	})
	return m, ports
}

func httpTestBed(name string) *bed.Bed {
	return bed.New(name, "", bed.Spec{Services: []bed.ServiceSpec{{
		Name: "web", Command: []string{"server"}, Env: map[string]string{"LISTEN": "${LISTEN_ADDR}"},
		Required: true, Restart: "never", StartupSeconds: 1, StopSeconds: 1,
		HTTP: &bed.ServiceHTTPSpec{ReadyPath: "/ready", TokenEnv: "TOKEN"},
	}}})
}

func httpTestRuntime(handler http.Handler) *fakeRuntime {
	var next int
	return &fakeRuntime{start: func(launch Launch) (Process, error) {
		ln, err := net.Listen("tcp", launch.Env["LISTEN"])
		if err != nil {
			return nil, err
		}
		next++
		p := &fakeProcess{id: fmt.Sprint(next), done: make(chan struct{})}
		server := &http.Server{Handler: handler}
		p.stop = func() { _ = server.Close() }
		go func() { _ = server.Serve(ln) }()
		return p, nil
	}}
}

func unavailableInspection(context.Context, int, string) (hostnetwork.ListenerInspection, error) {
	return hostnetwork.ListenerInspection{State: hostnetwork.ListenerUnavailable, Method: "proc", Reason: "permission_denied"}, nil
}

func TestSharedHTTPServiceDegradesAfterReadyAndRestarts(t *testing.T) {
	m, ports := httpTestManager(t)
	var degraded atomic.Bool
	m.inspectListener = func(ctx context.Context, pid int, addr string) (hostnetwork.ListenerInspection, error) {
		if degraded.Load() {
			return unavailableInspection(ctx, pid, addr)
		}
		return hostnetwork.ListenerInspection{State: hostnetwork.ListenerOwned, Method: "proc"}, nil
	}
	runtime := httpTestRuntime(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	b := httpTestBed("shared")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := m.PrepareBed(ctx, b, runtime); err != nil {
		t.Fatal(err)
	}
	first, err := m.Access(b, "web")
	if err != nil {
		t.Fatal(err)
	}
	degraded.Store(true)
	waitHTTPStatus(t, m, b, func(s Status) bool { return s.Listener != nil && s.Listener.State == hostnetwork.ListenerUnavailable })
	status := m.Status(b)[0]
	if status.Phase != "ready" || status.ExecutionID != first.ExecutionID || status.Restarts != 0 {
		t.Fatalf("inspection failure killed service: %+v", status)
	}
	resp, err := http.Get(first.Endpoint + "/ready")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("direct endpoint status=%d", resp.StatusCode)
	}
	if err := m.Restart(b, "web"); err != nil {
		t.Fatal(err)
	}
	waitHTTPStatus(t, m, b, func(s Status) bool { return s.Phase == "ready" && s.ExecutionID != first.ExecutionID })
	second, err := m.Access(b, "web")
	if err != nil {
		t.Fatal(err)
	}
	if second.Token == first.Token {
		t.Fatal("restart reused token")
	}
	if len(ports.Status()) != 1 {
		t.Fatalf("restart leaked ports: %+v", ports.Status())
	}
}

func TestSharedHTTPServicesHaveDistinctEndpoints(t *testing.T) {
	m, _ := httpTestManager(t)
	m.inspectListener = unavailableInspection
	var wg sync.WaitGroup
	endpoints := make(chan string, 4)
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := httpTestBed(fmt.Sprint(i))
			runtime := httpTestRuntime(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if err := m.PrepareBed(ctx, b, runtime); err != nil {
				t.Error(err)
				return
			}
			a, err := m.Access(b, "web")
			if err != nil {
				t.Error(err)
				return
			}
			endpoints <- a.Endpoint
		}()
	}
	wg.Wait()
	close(endpoints)
	seen := map[string]bool{}
	for endpoint := range endpoints {
		if seen[endpoint] {
			t.Errorf("duplicate endpoint %s", endpoint)
		}
		seen[endpoint] = true
	}
	if len(seen) != 4 {
		t.Fatalf("ready endpoints=%v", seen)
	}
}

func TestForeignListenerRetriesButUnavailableDoesNot(t *testing.T) {
	m, _ := httpTestManager(t)
	var calls atomic.Int32
	m.inspectListener = func(ctx context.Context, pid int, addr string) (hostnetwork.ListenerInspection, error) {
		if calls.Add(1) <= 2 {
			return hostnetwork.ListenerInspection{State: hostnetwork.ListenerForeign, Method: "proc"}, nil
		}
		return unavailableInspection(ctx, pid, addr)
	}
	runtime := httpTestRuntime(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	b := httpTestBed("conflict")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := m.PrepareBed(ctx, b, runtime); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.processes) != 2 {
		t.Fatalf("process starts=%d", len(runtime.processes))
	}
	select {
	case <-runtime.processes[0].Done():
	default:
		t.Fatal("conflicting execution not stopped")
	}
}

func TestUnavailableInspectionStillRequiresHTTPReadiness(t *testing.T) {
	m, _ := httpTestManager(t)
	m.inspectListener = unavailableInspection
	runtime := httpTestRuntime(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	b := httpTestBed("unhealthy")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := m.PrepareBed(ctx, b, runtime); err == nil {
		t.Fatal("unhealthy service ready")
	}
	status := m.Status(b)[0]
	if status.Endpoint != "" || !strings.Contains(status.Reason, "readiness timeout") {
		t.Fatalf("status=%+v", status)
	}
}

func TestListenerAbsentWaitsRatherThanConflicts(t *testing.T) {
	m, _ := httpTestManager(t)
	var calls atomic.Int32
	m.inspectListener = func(ctx context.Context, pid int, addr string) (hostnetwork.ListenerInspection, error) {
		if calls.Add(1) == 1 {
			return hostnetwork.ListenerInspection{State: hostnetwork.ListenerAbsent, Method: "proc"}, nil
		}
		return unavailableInspection(ctx, pid, addr)
	}
	runtime := httpTestRuntime(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	b := httpTestBed("starting")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := m.PrepareBed(ctx, b, runtime); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.processes) != 1 {
		t.Fatal("absence was treated as a conflict")
	}
}

func TestExitDuringProbeDoesNotPublishOrRetryAsConflict(t *testing.T) {
	m, _ := httpTestManager(t)
	m.inspectListener = unavailableInspection
	var runtime *fakeRuntime
	runtime = httpTestRuntime(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		runtime.mu.Lock()
		runtime.processes[0].exit(executor.Exited(1))
		runtime.mu.Unlock()
		w.WriteHeader(200)
	}))
	b := httpTestBed("exiting")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := m.PrepareBed(ctx, b, runtime); err == nil {
		t.Fatal("exited service published ready")
	}
	status := m.Status(b)[0]
	if status.Endpoint != "" || status.Reason != "process exited before readiness" {
		t.Fatalf("status=%+v", status)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.processes) != 1 {
		t.Fatal("exit was retried as a binding conflict")
	}
}

func waitHTTPStatus(t *testing.T, m *Manager, b *bed.Bed, predicate func(Status) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if predicate(m.Status(b)[0]) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("service status did not converge: %+v", m.Status(b))
}
