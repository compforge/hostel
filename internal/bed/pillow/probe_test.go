package pillow

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed"
)

func TestPortsAndProbeValidation(t *testing.T) {
	base := bed.PillowSpec{Name: "worker", Command: []string{"server"}, Ports: []bed.PillowPort{{Name: "http", ContainerPort: 18089}}, ReadinessProbe: &bed.Probe{HTTPGet: &bed.HTTPGetAction{Path: "/ready", Port: "http"}}}
	for _, tc := range []struct {
		name   string
		mutate func(*bed.PillowSpec)
	}{
		{"missing allocation", func(s *bed.PillowSpec) { s.Ports[0].ContainerPort = 0 }},
		{"fixed and dynamic", func(s *bed.PillowSpec) { s.Ports[0].Dynamic = true }},
		{"unsupported protocol", func(s *bed.PillowSpec) { s.Ports[0].Protocol = "UDP" }},
		{"numeric name", func(s *bed.PillowSpec) { s.Ports[0].Name = "123" }},
		{"foreign port", func(s *bed.PillowSpec) { s.ReadinessProbe.HTTPGet.Port = "main-http" }},
		{"unknown number", func(s *bed.PillowSpec) { s.ReadinessProbe.HTTPGet.Port = "18090" }},
		{"missing action", func(s *bed.PillowSpec) { s.ReadinessProbe.HTTPGet = nil }},
		{"two actions", func(s *bed.PillowSpec) { s.ReadinessProbe.TCPSocket = &bed.TCPSocketAction{Port: "http"} }},
		{"negative period", func(s *bed.PillowSpec) { s.ReadinessProbe.PeriodSeconds = -1 }},
		{"absolute URL", func(s *bed.PillowSpec) { s.ReadinessProbe.HTTPGet.Path = "http://example.org" }},
		{"no process", func(s *bed.PillowSpec) { s.Command = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := bed.ClonePillows([]bed.PillowSpec{base})
			tc.mutate(&input[0])
			if _, err := Normalize(input); err == nil {
				t.Fatal("invalid declaration accepted")
			}
		})
	}
	normalized, err := Normalize([]bed.PillowSpec{base})
	if err != nil {
		t.Fatal(err)
	}
	if normalized[0].Ports[0].Protocol != "TCP" || normalized[0].ReadinessProbe.FailureThreshold != 3 || normalized[0].ReadinessProbe.PeriodSeconds != 10 {
		t.Fatal("missing probe/port defaults")
	}
	recovered, err := Normalize(normalized)
	if err != nil || !reflect.DeepEqual(normalized, recovered) {
		t.Fatalf("unstable recovery: %v", err)
	}
	copy := bed.ClonePillows(normalized)
	copy[0].ReadinessProbe.HTTPGet.Path = "/changed"
	copy[0].Ports[0].Publish = true
	if normalized[0].ReadinessProbe.HTTPGet.Path != "/ready" || normalized[0].Ports[0].Publish {
		t.Fatal("mutable alias")
	}
	if _, err := Normalize(copy); err == nil {
		t.Fatal("digest did not pin ports and probe")
	}
	base.ReadinessProbe.HTTPGet.Port = "018089"
	if _, err := Normalize([]bed.PillowSpec{base}); err != nil {
		t.Fatalf("numeric declared port rejected: %v", err)
	}
	canonical, err := Normalize([]bed.PillowSpec{base})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip []bed.PillowSpec
	if err := json.Unmarshal(raw, &roundtrip); err != nil {
		t.Fatal(err)
	}
	recovered, err = Normalize(roundtrip)
	if err != nil || !reflect.DeepEqual(canonical, recovered) {
		t.Fatalf("numeric reference changed after recovery: %v", err)
	}
	other := bed.ClonePillows([]bed.PillowSpec{base})[0]
	other.Name = "main"
	other.Ports[0].Name = "other"
	if _, err := Normalize([]bed.PillowSpec{base, other}); err == nil {
		t.Fatal("duplicate fixed port in shared Bed accepted")
	}
}

func TestProbeThresholds(t *testing.T) {
	tracker := readinessTracker{}
	probe := &bed.Probe{SuccessThreshold: 2, FailureThreshold: 3}
	for i, step := range []struct{ success, ready bool }{
		{true, false}, {true, true}, {false, true}, {false, true}, {true, true},
		{false, true}, {false, true}, {false, false}, {true, false}, {true, true},
	} {
		got := tracker.observe(readinessResult{Ready: step.success, Reason: "failed"}, probe)
		if got.Ready != step.ready {
			t.Fatalf("step %d: ready=%t, want %t", i, got.Ready, step.ready)
		}
	}
}

// +case:id=pillow_multiple_ports,desc=`A Pillow publishes two dynamically allocated TCP listeners and uses a TCP readiness probe`,expect=`All ports bind the same Execution; opaque env is unchanged; restart and stop reclaim every allocation`
func TestMultiplePortsAndTCPReadiness(t *testing.T) {
	m, ports := httpTestManager(t)
	m.inspectListener = unavailableInspection
	token := "opaque-${PORT}-${LISTEN_ADDR_api}"
	spec := bed.PillowSpec{Name: "worker", Command: []string{"server", "${LISTEN_ADDR_api}", "${LISTEN_ADDR_metrics}"}, Env: map[string]string{"TOKEN": token}, Required: true,
		Ports:          []bed.PillowPort{{Name: "api", Dynamic: true, Publish: true}, {Name: "metrics", Dynamic: true}},
		ReadinessProbe: &bed.Probe{TCPSocket: &bed.TCPSocketAction{Port: "api"}, PeriodSeconds: 1},
	}
	var launches int
	runtime := &fakeRuntime{start: func(launch Launch) (Process, error) {
		if launch.Env["TOKEN"] != token {
			t.Error("opaque env was expanded")
		}
		var listeners []net.Listener
		for _, address := range launch.Command[1:] {
			ln, err := net.Listen("tcp", address)
			if err != nil {
				for _, l := range listeners {
					l.Close()
				}
				return nil, err
			}
			listeners = append(listeners, ln)
		}
		launches++
		p := &fakeProcess{id: string(rune('0' + launches)), done: make(chan struct{})}
		p.stop = func() {
			for _, ln := range listeners {
				ln.Close()
			}
		}
		return p, nil
	}}
	b := bed.New("multi", "", bed.Spec{Pillows: []bed.PillowSpec{spec}})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := m.PrepareBed(ctx, b, runtime); err != nil {
		t.Fatal(err)
	}
	first, err := m.Access(b, "worker")
	if err != nil || len(first.Ports) != 2 {
		t.Fatalf("access=%+v err=%v", first, err)
	}
	if first.Ports[0].HostPort == 0 || first.Ports[1].HostPort != 0 || first.Ports[0].ExecutionID != first.Ports[1].ExecutionID {
		t.Fatalf("publication=%+v", first)
	}
	encoded, _ := json.Marshal(first)
	var access map[string]any
	json.Unmarshal(encoded, &access)
	if _, exists := access["token"]; exists {
		t.Fatal("runtime exposed business auth")
	}
	if err := m.Restart(b, "worker"); err != nil {
		t.Fatal(err)
	}
	waitHTTPStatus(t, m, b, func(s Status) bool { return s.Ready && s.ExecutionID != first.Ports[0].ExecutionID })
	if len(ports.Status()) != 2 {
		t.Fatal("restart leaked allocations")
	}
	if err := m.Stop(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if len(ports.Status()) != 0 {
		t.Fatal("stop leaked allocations")
	}
}

func TestPartialPortReservationRollsBack(t *testing.T) {
	m, ports := httpTestManager(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	spec := bed.PillowSpec{Name: "main", Command: []string{"server"}, Required: true, Ports: []bed.PillowPort{
		{Name: "first", Dynamic: true}, {Name: "occupied", ContainerPort: occupied.Addr().(*net.TCPAddr).Port},
	}}
	b := bed.New("partial", "", bed.Spec{Pillows: []bed.PillowSpec{spec}})
	runtime := &fakeRuntime{}
	if err := m.PrepareBed(t.Context(), b, runtime); err == nil {
		t.Fatal("fixed port conflict silently downgraded")
	}
	if len(ports.Status()) != 0 {
		t.Fatal("partial reservation leaked")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.processes) != 0 {
		t.Fatal("process started with missing required port")
	}
}

func TestHTTPProbeDoesNotSendBusinessAuthorization(t *testing.T) {
	m, _ := httpTestManager(t)
	m.inspectListener = unavailableInspection
	runtime := httpTestRuntime(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("runtime attached business credentials")
		}
		w.WriteHeader(200)
	}))
	b := httpTestBed("no-auth")
	if err := m.PrepareBed(t.Context(), b, runtime); err != nil {
		t.Fatal(err)
	}
}
