package service

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/executor"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

func TestReadinessRetainsForwardingConnections(t *testing.T) {
	m, ports := httpTestManager(t)
	m.inspectListener = unavailableInspection
	var unavailable atomic.Bool
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	runtime := httpTestRuntime(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/task" {
			_, _ = io.WriteString(w, "started\n")
			w.(http.Flusher).Flush()
			select {
			case <-release:
				_, _ = io.WriteString(w, "completed\n")
			case <-r.Context().Done():
			}
			return
		}
		if unavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	// Exercise the actual TCPForwarder with a local backend, without claiming
	// a real Linux netns test. Its lifetime must follow the execution, not ready.
	runtime.scope = "test-scope"
	b := httpTestBed("forwarded")
	if err := m.PrepareBed(t.Context(), b, runtime); err != nil {
		t.Fatal(err)
	}
	first, err := m.Access(b, "web")
	if err != nil {
		t.Fatal(err)
	}
	allocations := ports.Status()
	if len(allocations) != 2 {
		t.Fatalf("expected backend and forwarder allocations: %+v", allocations)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, first.HostEndpoint+"/task", nil)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	if line, err := reader.ReadString('\n'); err != nil || line != "started\n" {
		t.Fatalf("task did not start: %q %v", line, err)
	}
	unavailable.Store(true)
	waitHTTPStatus(t, m, b, func(s Status) bool { return !s.Ready && s.Reason == "ReadinessHTTPStatus503" })
	if _, err := m.Access(b, "web"); err == nil || m.Ready(b) {
		t.Fatal("unready service admitted access or marked required services ready")
	}
	currentPorts := ports.Status()
	if len(currentPorts) != len(allocations) || !slices.Contains(currentPorts, allocations[0]) || !slices.Contains(currentPorts, allocations[1]) {
		t.Fatal("readiness replaced allocations")
	}
	unavailable.Store(false)
	waitHTTPStatus(t, m, b, func(s Status) bool { return s.Ready })
	current, err := m.Access(b, "web")
	if err != nil || current != first {
		t.Fatalf("changed run on recovery: %+v %v", current, err)
	}
	once.Do(func() { close(release) })
	if rest, err := io.ReadAll(reader); err != nil || string(rest) != "completed\n" {
		t.Fatalf("forwarder interrupted task: %q %v", rest, err)
	}
}

func TestTransientObservationsDoNotTerminateExecution(t *testing.T) {
	m, _ := httpTestManager(t)
	var mode atomic.Int32
	m.inspectListener = func(ctx context.Context, pid int, address string) (hostnetwork.ListenerInspection, error) {
		switch mode.Load() {
		case 1:
			return hostnetwork.ListenerInspection{State: hostnetwork.ListenerAbsent}, nil
		case 2:
			return hostnetwork.ListenerInspection{}, context.DeadlineExceeded
		}
		return unavailableInspection(ctx, pid, address)
	}
	runtime := httpTestRuntime(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode.Load() == 3 {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	b := httpTestBed("transient")
	if err := m.PrepareBed(t.Context(), b, runtime); err != nil {
		t.Fatal(err)
	}
	first, _ := m.Access(b, "web")
	for _, tc := range []struct {
		mode   int32
		reason string
	}{{1, "ListenerAbsent"}, {2, "ListenerInspectionFailed"}, {3, "ReadinessProbeFailed"}} {
		mode.Store(tc.mode)
		waitHTTPStatus(t, m, b, func(s Status) bool { return !s.Ready && s.Reason == tc.reason })
		status := m.Status(b)[0]
		if status.Phase != "running" || status.ExecutionID != first.ExecutionID || status.Restarts != 0 {
			t.Fatalf("observation ended execution: %+v", status)
		}
		mode.Store(0)
		waitHTTPStatus(t, m, b, func(s Status) bool { return s.Ready && s.Reason == "" })
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.processes) != 1 {
		t.Fatal("transient observation restarted the service")
	}
}

func TestActualExitRetainsRestartPolicy(t *testing.T) {
	for _, tc := range []struct {
		policy  string
		exit    int
		restart bool
	}{{"never", 1, false}, {"on-failure", 0, false}, {"on-failure", 1, true}, {"always", 0, true}} {
		t.Run(fmt.Sprintf("%s/%d", tc.policy, tc.exit), func(t *testing.T) {
			m, _ := testController(t, true)
			b := newServiceTestBed("policy", "", bed.Spec{Services: []bed.ServiceSpec{{Name: "worker", Command: []string{"worker"}, Required: true, Restart: tc.policy, MaxRestarts: 1}}})
			runtime := &fakeRuntime{}
			if err := m.PrepareBed(t.Context(), b, runtime); err != nil {
				t.Fatal(err)
			}
			runtime.mu.Lock()
			first := runtime.processes[0]
			runtime.mu.Unlock()
			first.exit(executor.Exited(tc.exit))
			waitHTTPStatus(t, m, b, func(s Status) bool {
				if tc.restart {
					return s.Ready && s.ExecutionID != first.id && s.Restarts == 1
				}
				return s.Phase == "failed" && !s.Ready && s.Outcome != nil && s.Outcome.ExitCode == tc.exit
			})
		})
	}
}

func TestRuntimeListenerConflictStillEndsExecution(t *testing.T) {
	m, _ := httpTestManager(t)
	var foreign atomic.Bool
	m.inspectListener = func(ctx context.Context, pid int, address string) (hostnetwork.ListenerInspection, error) {
		if foreign.Load() {
			return hostnetwork.ListenerInspection{State: hostnetwork.ListenerForeign}, nil
		}
		return unavailableInspection(ctx, pid, address)
	}
	runtime := httpTestRuntime(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	b := httpTestBed("foreign")
	if err := m.PrepareBed(t.Context(), b, runtime); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	first := runtime.processes[0]
	runtime.mu.Unlock()
	foreign.Store(true)
	waitHTTPStatus(t, m, b, func(s Status) bool { return s.Phase == "failed" && !s.Ready })
	select {
	case <-first.Done():
	default:
		t.Fatal("confirmed foreign listener did not end conflicting execution")
	}
}
