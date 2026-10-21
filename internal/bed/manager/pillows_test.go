package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/bed/pillow"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

// The helper is an ordinary foreground HTTP pillow: no Hostel registration
// callback. It authenticates requests and observes TERM before returning.
func TestPillowHelperProcess(t *testing.T) {
	if os.Getenv("SERVICE_TEST_HELPER") != "1" {
		return
	}
	listener, err := net.Listen("tcp", os.Getenv("SERVICE_LISTEN"))
	if err != nil {
		os.Exit(32)
	}
	_ = os.WriteFile("pillow-started", []byte(os.Getenv("BED_ID")), 0600)
	_ = os.WriteFile("pillow-credential", []byte(os.Getenv("SERVICE_FILE_VALUE")), 0600)
	var unavailable atomic.Bool
	releaseTask := make(chan struct{})
	var releaseOnce sync.Once
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := ""
		if token := os.Getenv("SERVICE_TOKEN"); token != "" {
			expected = "Bearer " + token
		}
		if r.Header.Get("Authorization") != expected {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/exit" {
			go func() { time.Sleep(10 * time.Millisecond); os.Exit(1) }()
		}
		switch r.URL.Path {
		case "/unready":
			unavailable.Store(true)
		case "/recover":
			unavailable.Store(false)
		case "/ready":
			if unavailable.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		case "/task":
			_, _ = fmt.Fprintf(w, "started %d\n", os.Getpid())
			w.(http.Flusher).Flush()
			select {
			case <-releaseTask:
				_, _ = fmt.Fprintf(w, "completed %d\n", os.Getpid())
			case <-r.Context().Done():
			}
			return
		case "/release-task":
			releaseOnce.Do(func() { close(releaseTask) })
		}
		_, _ = io.WriteString(w, "ready")
	})}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	go srv.Serve(listener)
	fmt.Println("test pillow started")
	<-ctx.Done()
	_ = os.WriteFile("pillow-stopped", []byte("graceful"), 0600)
	_ = srv.Close()
	os.Exit(0)
}

func testPillowManager(t *testing.T) (*Manager, []model.PillowSpec, *hostnetwork.PortManager) {
	t.Helper()
	ports, _ := hostnetwork.NewPortManager(25000, 25100)
	m := newTestManager(t)
	WithPillows(ports)(m)
	credential := filepath.Join(t.TempDir(), "pillow-value")
	if err := os.WriteFile(credential, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
		ports.Close()
	})
	pillowSpec := model.PillowSpec{
		Command:        []string{os.Args[0], "-test.run=^TestPillowHelperProcess$"},
		Env:            map[string]string{"SERVICE_TEST_HELPER": "1", "SERVICE_LISTEN": "${LISTEN_ADDR}", "GORACE": "atexit_sleep_ms=0"},
		EnvFiles:       map[string]string{"SERVICE_FILE_VALUE": credential},
		Required:       true,
		RestartPolicy:  "on-failure",
		MaxRestarts:    2,
		StartupSeconds: 10,
		StopSeconds:    1,
		HTTP:           &model.PillowHTTPSpec{ReadyPath: "/ready", Authentication: &model.Authentication{Scheme: "bearer", TokenSource: model.TokenSourceGenerated, TokenEnv: "SERVICE_TOKEN"}},
	}
	main, tools := pillowSpec, pillowSpec
	main.Name, tools.Name = "main", "tools"
	main.PortMapping, tools.PortMapping = "main", "tools"
	return m, []model.PillowSpec{main, tools}, ports
}

func waitPillow(t *testing.T, m *Manager, b *Resident, name string, predicate func(pillow.Status) bool) pillow.Status {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range m.pillows.Status(b.Bed) {
			if s.Name == name && predicate(s) {
				return s
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pillow state timeout: %+v", m.pillows.Status(b.Bed))
	return pillow.Status{}
}

func TestBedPillowsShareEnvironmentAndRemainOptionalPerBed(t *testing.T) {
	m, specs, ports := testPillowManager(t)
	if _, err := m.InitializeBedWithOptions(t.Context(), "with-pillows", testPillowOptions(specs)); err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(t.Context(), "with-pillows")
	if err != nil {
		t.Fatal(err)
	}
	if !b.Bed.Status().Lifecycle.Ready || b.Status().Activity != ActivityIdle {
		t.Fatalf("pillows pinned bed: %+v", b.Status())
	}
	a, _ := m.pillows.Access(b.Bed, "main")
	other, _ := m.pillows.Access(b.Bed, "tools")
	if testPillowURL(a.PortMapping.HostPort) == testPillowURL(other.PortMapping.HostPort) || a.Token == other.Token || a.PortMapping.ExecutionID == other.PortMapping.ExecutionID {
		t.Fatal("pillow instances share runtime identity")
	}
	data, err := os.ReadFile(filepath.Join(b.Workdir(), "pillow-started"))
	if err != nil || string(data) != b.Name {
		t.Fatalf("pillow did not use Bed environment: %q %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(b.Workdir(), "pillow-credential"))
	if err != nil || string(data) != "from-file" {
		t.Fatalf("pillow did not resolve credential file: %q %v", data, err)
	}
	if _, err := m.InitializeBed(t.Context(), b.Name); !errors.Is(err, ErrPillowsConflict) {
		t.Fatalf("explicit empty create changed pillows: %v", err)
	}
	if _, err := m.InitializeBedWithOptions(t.Context(), b.Name, testPillowOptions(specs)); err != nil {
		t.Fatal(err)
	}
	plain, err := m.Ensure(t.Context(), "plain")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.pillows.Status(plain.Bed)) != 1 || m.pillows.Status(plain.Bed)[0].Phase != "prepared" || plain.Bed.Status().Executor.ID != "" {
		t.Fatal("plain Bed eagerly created a pillow/executor")
	}
	if len(ports.Status()) != 2 {
		t.Fatalf("allocations: %+v", ports.Status())
	}
	encoded, _ := json.Marshal(m.pillows.Status(b.Bed))
	identity, _ := os.ReadFile(m.identityPath(b.Name))
	if strings.Contains(string(encoded), a.Token) || strings.Contains(string(identity), a.Token) {
		t.Fatal("runtime credentials leaked to status/local metadata")
	}
	if err := m.pillows.Stop(t.Context(), b.Bed); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(b.Workdir(), "pillow-stopped"))
	if err != nil || string(data) != "graceful" {
		t.Fatalf("TERM not honored: %q %v", data, err)
	}
	if len(ports.Status()) != 0 {
		t.Fatalf("ports retained after stopping: %+v", ports.Status())
	}
}

func TestBedPillowRestartChangesIdentityAndToken(t *testing.T) {
	m, specs, _ := testPillowManager(t)
	_, err := m.InitializeBedWithOptions(t.Context(), "restart", testPillowOptions(specs[:1]))
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(t.Context(), "restart")
	if err != nil {
		t.Fatal(err)
	}
	old, _ := m.pillows.Access(b.Bed, "main")
	if err := m.pillows.Restart(b.Bed, "main"); err != nil {
		t.Fatal(err)
	}
	waitPillow(t, m, b, "main", func(s pillow.Status) bool {
		return s.Phase == "running" && s.Ready && s.ExecutionID != old.PortMapping.ExecutionID
	})
	current, _ := m.pillows.Access(b.Bed, "main")
	if old.Token == current.Token {
		t.Fatal("restarted pillow reused token")
	}
	req, _ := http.NewRequest(http.MethodGet, testPillowURL(current.PortMapping.HostPort)+"/ready", nil)
	req.Header.Set("Authorization", "Bearer "+old.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatal("old credentials authorized new execution")
	}
	if _, err := m.HoldPillow(b, 150*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if evicted, err := m.Evict(t.Context(), b.Name); err != nil || evicted {
		t.Fatalf("evicted active direct-access hold: %t %v", evicted, err)
	}
	time.Sleep(200 * time.Millisecond)
	if evicted, err := m.Evict(t.Context(), b.Name); err != nil || !evicted {
		t.Fatalf("idle pillow prevented eviction: %t %v", evicted, err)
	}
}

func TestBedPillowExecutionsUseCommonRegistry(t *testing.T) {
	m, specs, _ := testPillowManager(t)
	_, err := m.InitializeBedWithOptions(t.Context(), "execution", testPillowOptions(specs[:1]))
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(t.Context(), "execution")
	if err != nil {
		t.Fatal(err)
	}
	access, _ := m.pillows.Access(b.Bed, "main")
	e, ok := m.executions.Get(access.PortMapping.ExecutionID)
	if !ok || e.Mode != ExecutionMain || e.ExecutorID != b.Bed.Status().Executor.ID {
		t.Fatal("pillow bypassed execution registry")
	}
	if err := m.pillows.Stop(t.Context(), b.Bed); err != nil {
		t.Fatal(err)
	}
	if result := e.Wait(); result.Process.Kind != executor.ProcessExited || result.Process.ExitCode != 0 {
		t.Fatalf("wrong graceful exit: %+v", result)
	}
}

func TestBedPillowNeverPublishesSquatterAndRetriesBinding(t *testing.T) {
	m, specs, _ := testPillowManager(t)
	squatter, err := net.Listen("tcp", "0.0.0.0:25000")
	if err != nil {
		t.Fatal(err)
	}
	defer squatter.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "foreign ready") })}
	go srv.Serve(squatter)
	defer srv.Close()
	if _, err := m.InitializeBedWithOptions(t.Context(), "squatter", testPillowOptions(specs[:1])); err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(t.Context(), "squatter")
	if err != nil {
		t.Fatal(err)
	}
	s := m.pillows.Status(b.Bed)[0]
	if m.PortMappings(b.Bed)[0].HostPort == 25000 || s.Restarts != 0 {
		t.Fatalf("binding conflict was not retried independently: %+v", s)
	}
}

func TestBedPillowHoldCanBeReleasedEarly(t *testing.T) {
	m, specs, _ := testPillowManager(t)
	_, err := m.InitializeBedWithOptions(t.Context(), "hold", testPillowOptions(specs[:1]))
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(t.Context(), "hold")
	if err != nil {
		t.Fatal(err)
	}
	hold, err := m.HoldPillow(b, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	other, err := m.Ensure(t.Context(), "other")
	if err != nil {
		t.Fatal(err)
	}
	m.ReleasePillowHold(other, hold.ID)
	if b.Status().Activity != ActivityActive {
		t.Fatal("wrong Bed released hold")
	}
	m.ReleasePillowHold(b, hold.ID)
	m.ReleasePillowHold(b, hold.ID)
	if b.Status().Activity != ActivityIdle {
		t.Fatal("hold did not release")
	}
}

func TestBedPillowLocalRecoveryUsesPersistedDeclaration(t *testing.T) {
	m, specs, ports := testPillowManager(t)
	_, err := m.InitializeBedWithOptions(t.Context(), "recover", testPillowOptions(specs[:1]))
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(t.Context(), "recover")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The complete desired state lives with the local Bed identity. Recovery no
	// longer depends on a deployment-owned template catalog.
	host := hostfacts.Collect()
	next, err := NewManager(host, m.root, "default", "/bin/sh", isolation.New(host, "shared", m.root), nil, 0, nil, WithPillows(ports))
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close(context.Background())
	if err := next.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	next.mu.Lock()
	local := next.localIdentities[b.Name]
	identityRetained := local != nil && local.bed.ID == b.ID
	next.mu.Unlock()
	if !identityRetained {
		t.Fatal("recovery replaced local Bed identity")
	}
	recovered, err := next.Ensure(t.Context(), b.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got := recovered.Spec().PortMappings; len(got) != 1 || got[0].Name != "main" || !got[0].Publish {
		t.Fatalf("lost port declaration: %+v", got)
	}
	if got := recovered.Spec().Pillows; len(got) != 1 || got[0].Name != "main" || got[0].SpecDigest == "" {
		t.Fatalf("recovery lost pillow declaration: %+v", got)
	}
}

func testPillowOptions(specs []model.PillowSpec) CreateOptions {
	options := CreateOptions{Pillows: specs}
	for _, s := range specs {
		options.PortMappings = append(options.PortMappings, model.PortMappingSpec{Name: s.PortMapping, Protocol: "tcp", Publish: true})
	}
	return options
}

func testPillowURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}
