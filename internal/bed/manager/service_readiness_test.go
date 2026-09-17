package manager

import (
	"bufio"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/service"
)

// +case:id=service_readiness_continuity,desc=`A real Service returns 200, then 503, then 200 during one HTTP task`,expect=`Same process, execution, credentials, allocation and hold; actual exit still restarts`,forbid=`Readiness loss interrupts the task`
func TestServiceReadinessPreservesRunningTask(t *testing.T) {
	m, specs, ports := testServiceManager(t)
	if _, err := m.InitializeBedWithOptions(t.Context(), "readiness", testServiceOptions(specs[:1])); err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(t.Context(), "readiness")
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.services.Access(b.Bed, "main")
	if err != nil {
		t.Fatal(err)
	}
	before := m.services.Status(b.Bed)[0]
	allocations := ports.Status()
	hold, err := m.HoldService(b, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer m.ReleaseServiceHold(b, hold.ID)
	client := &http.Client{Timeout: 15 * time.Second}
	request := func(path string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, first.HostEndpoint+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+first.Token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("%s: HTTP %d", path, resp.StatusCode)
		}
		return resp
	}
	task := request("/task")
	defer task.Body.Close()
	reader := bufio.NewReader(task.Body)
	started, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(started, "started ") {
		t.Fatalf("task did not start: %q %v", started, err)
	}
	request("/unready").Body.Close()
	unready := waitService(t, m, b, "main", func(s service.Status) bool {
		return !s.Ready && s.Reason == "ReadinessHTTPStatus503" && !b.Bed.Status().Lifecycle.Ready
	})
	if unready.Phase != "running" || unready.ExecutionID != before.ExecutionID || unready.ExecutorID != before.ExecutorID || unready.HostEndpoint != before.HostEndpoint || unready.Restarts != before.Restarts || unready.Outcome != nil {
		t.Fatalf("readiness changed run identity: before=%+v after=%+v", before, unready)
	}
	if !reflect.DeepEqual(ports.Status(), allocations) {
		t.Fatalf("readiness changed allocations: %+v", ports.Status())
	}
	if _, err := m.services.Access(b.Bed, "main"); err == nil {
		t.Fatal("unready service admitted new access")
	}
	if b.Status().Activity != ActivityActive || b.Bed.Status().Lifecycle.Phase != PhaseResident {
		t.Fatal("readiness revoked existing operation or resident Bed")
	}
	if evicted, err := m.Evict(t.Context(), b.Name); err != nil || evicted {
		t.Fatalf("unready service lost its hold: %t %v", evicted, err)
	}
	execution, ok := m.executions.Get(first.ExecutionID)
	if !ok {
		t.Fatal("missing execution")
	}
	select {
	case <-execution.done:
		t.Fatal("readiness ended execution")
	default:
	}
	// Existing direct clients remain usable; readiness gates discovery, not
	// all traffic to an address that has already been returned to a caller.
	request("/recover").Body.Close()
	waitService(t, m, b, "main", func(s service.Status) bool { return s.Ready && b.Bed.Status().Lifecycle.Ready })
	recovered, err := m.services.Access(b.Bed, "main")
	if err != nil || recovered != first {
		t.Fatalf("recovery changed access: %+v %v", recovered, err)
	}
	request("/release-task").Body.Close()
	completed, err := io.ReadAll(reader)
	if err != nil || string(completed) != strings.Replace(started, "started ", "completed ", 1) {
		t.Fatalf("in-flight task interrupted or replaced: %q %v", completed, err)
	}
	request("/exit").Body.Close()
	next := waitService(t, m, b, "main", func(s service.Status) bool { return s.Ready && s.ExecutionID != first.ExecutionID })
	if next.Restarts != 1 {
		t.Fatalf("real exit did not consume exactly one restart: %+v", next)
	}
	access, err := m.services.Access(b.Bed, "main")
	if err != nil || access.Token == first.Token {
		t.Fatal("actual restart did not rotate credentials")
	}
}
