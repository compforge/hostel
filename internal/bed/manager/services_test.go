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
	"syscall"
	"testing"
	"time"

	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/bed/service"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

// The helper is an ordinary foreground HTTP service: no Hostel registration
// callback. It authenticates requests and observes TERM before returning.
func TestServiceHelperProcess(t *testing.T) {
	if os.Getenv("SERVICE_TEST_HELPER") != "1" {
		return
	}
	listener, err := net.Listen("tcp", os.Getenv("SERVICE_LISTEN"))
	if err != nil {
		os.Exit(32)
	}
	_ = os.WriteFile("service-started", []byte(os.Getenv("BED_ID")), 0600)
	_ = os.WriteFile("service-credential", []byte(os.Getenv("SERVICE_FILE_VALUE")), 0600)
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
		_, _ = io.WriteString(w, "ready")
	})}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	go srv.Serve(listener)
	fmt.Println("test service started")
	<-ctx.Done()
	_ = os.WriteFile("service-stopped", []byte("graceful"), 0600)
	_ = srv.Close()
	os.Exit(0)
}

func testServiceManager(t *testing.T) (*Manager, []model.ServiceSpec, *hostnetwork.PortManager) {
	t.Helper()
	ports, _ := hostnetwork.NewPortManager(25000, 25100)
	m := newTestManager(t)
	WithServices(ports, "127.0.0.1")(m)
	credential := filepath.Join(t.TempDir(), "service-value")
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
	serviceSpec := model.ServiceSpec{
		Command:        []string{os.Args[0], "-test.run=^TestServiceHelperProcess$"},
		Env:            map[string]string{"SERVICE_TEST_HELPER": "1", "SERVICE_LISTEN": "${LISTEN_ADDR}", "GORACE": "atexit_sleep_ms=0"},
		EnvFiles:       map[string]string{"SERVICE_FILE_VALUE": credential},
		Required:       true,
		Restart:        "on-failure",
		MaxRestarts:    2,
		StartupSeconds: 10,
		StopSeconds:    1,
		HTTP:           &model.ServiceHTTPSpec{ReadyPath: "/ready", Authentication: &model.Authentication{Scheme: "bearer", TokenSource: model.TokenSourceGenerated, TokenEnv: "SERVICE_TOKEN"}},
	}
	main, tools := serviceSpec, serviceSpec
	main.Name, tools.Name = "main", "tools"
	return m, []model.ServiceSpec{main, tools}, ports
}

func waitService(t *testing.T, m *Manager, b *Resident, name string, predicate func(service.Status) bool) service.Status {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range m.services.Status(b.Bed) {
			if s.Name == name && predicate(s) {
				return s
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("service state timeout: %+v", m.services.Status(b.Bed))
	return service.Status{}
}

func TestBedServicesShareEnvironmentAndRemainOptionalPerBed(t *testing.T) {
	m, specs, ports := testServiceManager(t)
	if _, err := m.InitializeBedWithOptions(t.Context(), "with-services", CreateOptions{Services: specs}); err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(t.Context(), "with-services")
	if err != nil {
		t.Fatal(err)
	}
	if !b.Bed.Status().Lifecycle.Ready || b.Status().Activity != ActivityIdle {
		t.Fatalf("services pinned bed: %+v", b.Status())
	}
	a, _ := m.services.Access(b.Bed, "main")
	other, _ := m.services.Access(b.Bed, "tools")
	if a.Endpoint == other.Endpoint || a.Token == other.Token || a.ExecutionID == other.ExecutionID {
		t.Fatal("service instances share runtime identity")
	}
	data, err := os.ReadFile(filepath.Join(b.Workspace(), "service-started"))
	if err != nil || string(data) != b.Name {
		t.Fatalf("service did not use Bed environment: %q %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(b.Workspace(), "service-credential"))
	if err != nil || string(data) != "from-file" {
		t.Fatalf("service did not resolve credential file: %q %v", data, err)
	}
	if _, err := m.InitializeBed(t.Context(), b.Name); !errors.Is(err, ErrServicesConflict) {
		t.Fatalf("explicit empty create changed services: %v", err)
	}
	if _, err := m.InitializeBedWithOptions(t.Context(), b.Name, CreateOptions{Services: specs}); err != nil {
		t.Fatal(err)
	}
	plain, err := m.Ensure(t.Context(), "plain")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.services.Status(plain.Bed)) != 0 || plain.Bed.Status().Executor.ID != "" {
		t.Fatal("plain Bed eagerly created a service/executor")
	}
	if len(ports.Status()) != 2 {
		t.Fatalf("allocations: %+v", ports.Status())
	}
	encoded, _ := json.Marshal(m.services.Status(b.Bed))
	identity, _ := os.ReadFile(m.identityPath(b.Name))
	if strings.Contains(string(encoded), a.Token) || strings.Contains(string(identity), a.Token) {
		t.Fatal("runtime credentials leaked to status/local metadata")
	}
	if err := m.services.Stop(t.Context(), b.Bed); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(b.Workspace(), "service-stopped"))
	if err != nil || string(data) != "graceful" {
		t.Fatalf("TERM not honored: %q %v", data, err)
	}
	if len(ports.Status()) != 0 {
		t.Fatalf("ports retained after stopping: %+v", ports.Status())
	}
}

func TestBedServiceRestartChangesIdentityAndToken(t *testing.T) {
	m, specs, _ := testServiceManager(t)
	_, err := m.InitializeBedWithOptions(t.Context(), "restart", CreateOptions{Services: specs[:1]})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(t.Context(), "restart")
	if err != nil {
		t.Fatal(err)
	}
	old, _ := m.services.Access(b.Bed, "main")
	if err := m.services.Restart(b.Bed, "main"); err != nil {
		t.Fatal(err)
	}
	waitService(t, m, b, "main", func(s service.Status) bool { return s.Phase == "ready" && s.ExecutionID != old.ExecutionID })
	current, _ := m.services.Access(b.Bed, "main")
	if old.Token == current.Token {
		t.Fatal("restarted service reused token")
	}
	req, _ := http.NewRequest(http.MethodGet, current.Endpoint+"/ready", nil)
	req.Header.Set("Authorization", "Bearer "+old.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatal("old credentials authorized new execution")
	}
	if _, err := m.HoldService(b, 150*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if evicted, err := m.Evict(t.Context(), b.Name); err != nil || evicted {
		t.Fatalf("evicted active direct-access hold: %t %v", evicted, err)
	}
	time.Sleep(200 * time.Millisecond)
	if evicted, err := m.Evict(t.Context(), b.Name); err != nil || !evicted {
		t.Fatalf("idle service prevented eviction: %t %v", evicted, err)
	}
}

func TestBedServiceExecutionsUseCommonRegistry(t *testing.T) {
	m, specs, _ := testServiceManager(t)
	_, err := m.InitializeBedWithOptions(t.Context(), "execution", CreateOptions{Services: specs[:1]})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(t.Context(), "execution")
	if err != nil {
		t.Fatal(err)
	}
	access, _ := m.services.Access(b.Bed, "main")
	e, ok := m.executions.Get(access.ExecutionID)
	if !ok || e.Mode != ExecutionService || e.ExecutorID != b.Bed.Status().Executor.ID {
		t.Fatal("service bypassed execution registry")
	}
	if err := m.services.Stop(t.Context(), b.Bed); err != nil {
		t.Fatal(err)
	}
	if result := e.Wait(); result.Process.Kind != executor.ProcessExited || result.Process.ExitCode != 0 {
		t.Fatalf("wrong graceful exit: %+v", result)
	}
}

func TestBedServiceNeverPublishesSquatterAndRetriesBinding(t *testing.T) {
	m, specs, _ := testServiceManager(t)
	squatter, err := net.Listen("tcp", "0.0.0.0:25000")
	if err != nil {
		t.Fatal(err)
	}
	defer squatter.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "foreign ready") })}
	go srv.Serve(squatter)
	defer srv.Close()
	if _, err := m.InitializeBedWithOptions(t.Context(), "squatter", CreateOptions{Services: specs[:1]}); err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(t.Context(), "squatter")
	if err != nil {
		t.Fatal(err)
	}
	s := m.services.Status(b.Bed)[0]
	if strings.HasSuffix(s.Endpoint, ":25000") || s.Restarts != 0 {
		t.Fatalf("binding conflict was not retried independently: %+v", s)
	}
}

func TestBedServiceHoldCanBeReleasedEarly(t *testing.T) {
	m, specs, _ := testServiceManager(t)
	_, err := m.InitializeBedWithOptions(t.Context(), "hold", CreateOptions{Services: specs[:1]})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(t.Context(), "hold")
	if err != nil {
		t.Fatal(err)
	}
	hold, err := m.HoldService(b, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	other, err := m.Ensure(t.Context(), "other")
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseServiceHold(other, hold.ID)
	if b.Status().Activity != ActivityActive {
		t.Fatal("wrong Bed released hold")
	}
	m.ReleaseServiceHold(b, hold.ID)
	m.ReleaseServiceHold(b, hold.ID)
	if b.Status().Activity != ActivityIdle {
		t.Fatal("hold did not release")
	}
}

func TestBedServiceLocalRecoveryUsesPersistedDeclaration(t *testing.T) {
	m, specs, ports := testServiceManager(t)
	_, err := m.InitializeBedWithOptions(t.Context(), "recover", CreateOptions{Services: specs[:1]})
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
	next, err := NewManager(host, m.root, "default", "/bin/sh", isolation.New(host, "shared", m.root), nil, 0, nil, WithServices(ports, "127.0.0.1"))
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
	if got := recovered.Spec().Services; len(got) != 1 || got[0].Name != "main" || got[0].SpecDigest == "" {
		t.Fatalf("recovery lost service declaration: %+v", got)
	}
}
