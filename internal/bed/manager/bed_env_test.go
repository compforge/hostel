package manager

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/bed/service"
	"github.com/qiankunli/hostel/internal/bed/store"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

func TestBedEnvExecutionAndSessionOwnership(t *testing.T) {
	m := newTestManager(t)
	defer m.Close(context.Background())
	m.SetCarrierEnvironment([]string{"PATH=/usr/bin:/bin", "LANG=C", "VALUE=carrier"})
	declared := map[string]string{"VALUE": "base", "BASE_ONLY": "present"}
	if _, err := m.InitializeBedWithOptions(t.Context(), "env", CreateOptions{Env: declared}); err != nil {
		t.Fatal(err)
	}
	declared["VALUE"] = "caller-mutated"
	b, err := m.Ensure(t.Context(), "env")
	if err != nil {
		t.Fatal(err)
	}
	run := func(overlay map[string]string, want string) {
		t.Helper()
		var out strings.Builder
		result, err := m.RunForeground(t.Context(), b, `printf '%s/%s/%s' "$VALUE" "$BASE_ONLY" "$LANG"`, "", overlay, time.Second, func(o ExecutionOutput) { out.WriteString(o.Text) })
		if err != nil || result.Process.Kind != executor.ProcessExited || result.Process.ExitCode != 0 || out.String() != want {
			t.Fatalf("execution: result=%+v err=%v output=%q", result, err, out.String())
		}
	}
	run(map[string]string{"VALUE": "once"}, "once/present/C")
	run(map[string]string{"VALUE": ""}, "/present/C")
	run(nil, "base/present/C")
	background, err := m.StartExecution(t.Context(), b, ExecutionBackground, `printf '%s' "$VALUE"`, "", "", map[string]string{"VALUE": "background"}, time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result := background.Wait(); result.Process.ExitCode != 0 {
		t.Fatalf("background: %+v", result)
	}
	output, _, _, _ := background.Logs(-1)
	if executionOutputText(output) != "background" {
		t.Fatal("background did not apply execution environment")
	}
	sh, err := m.ForegroundShell(b)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if _, err := sh.Run(t.Context(), `printf '%s\n' "$VALUE"; export VALUE=session`, func(s string) { out.WriteString(s) }); err != nil || strings.TrimSpace(out.String()) != "base" {
		t.Fatalf("session base environment: %q %v", out.String(), err)
	}
	out.Reset()
	if _, err := sh.Run(t.Context(), `printf '%s\n' "$VALUE"`, func(s string) { out.WriteString(s) }); err != nil || strings.TrimSpace(out.String()) != "session" {
		t.Fatalf("session environment state: %q %v", out.String(), err)
	}
	run(nil, "base/present/C")
	id, err := m.CreateShell(b, "")
	if err != nil {
		t.Fatal(err)
	}
	independent, ok := b.GetShell(id)
	if !ok {
		t.Fatal("created shell missing")
	}
	out.Reset()
	if _, err := independent.Run(t.Context(), `printf '%s\n' "$VALUE"`, func(s string) { out.WriteString(s) }); err != nil || strings.TrimSpace(out.String()) != "base" {
		t.Fatalf("independent shell environment: %q %v", out.String(), err)
	}
	oldExecutor := m.executorManager.Current(b.Bed)
	if err := oldExecutor.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	run(nil, "base/present/C")
	if m.executorManager.Current(b.Bed).ID() == oldExecutor.ID() {
		t.Fatal("executor was not replaced")
	}
	other, err := m.Ensure(t.Context(), "other")
	if err != nil {
		t.Fatal(err)
	}
	env, err := m.buildExecutionEnv(other, nil)
	if err != nil || envMap(env)["VALUE"] != "carrier" || envMap(env)["BASE_ONLY"] != "" {
		t.Fatalf("Bed environment leaked into another Bed: %v", err)
	}
}

func TestBedEnvDeclarationConflictAndRecovery(t *testing.T) {
	m := newTestManager(t)
	defer m.Close(context.Background())
	want := map[string]string{"VALUE": "local-only-value"}
	if _, err := m.InitializeBedWithOptions(t.Context(), "recover-env", CreateOptions{Env: want}); err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(t.Context(), "recover-env")
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range []map[string]string{nil, {}, {"VALUE": "changed"}} {
		if _, err := m.InitializeBedWithOptions(t.Context(), b.Name, CreateOptions{Env: env}); !errors.Is(err, ErrEnvConflict) {
			t.Fatalf("changed declaration accepted: %v", err)
		}
	}
	if _, err := m.InitializeBedWithOptions(t.Context(), b.Name, CreateOptions{Env: want}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(m.identityPath(b.Name))
	if err != nil {
		t.Fatal(err)
	}
	var record localIdentityRecord
	if err := json.Unmarshal(data, &record); err != nil || !maps.Equal(record.Env, want) {
		t.Fatalf("environment not persisted: %v", err)
	}
	info, err := os.Stat(m.identityPath(b.Name))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("identity permissions: %v %v", info, err)
	}
	if err := filepath.WalkDir(b.Spec().Dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		contents, err := os.ReadFile(path)
		if strings.Contains(string(contents), want["VALUE"]) {
			t.Errorf("environment stored in portable Bed tree: %s", path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	host := hostfacts.Collect()
	next, err := NewManager(host, m.root, "default", "/bin/bash", isolation.New(host, "shared", m.root), nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close(context.Background())
	if _, err := next.InitializeBedWithOptions(t.Context(), b.Name, CreateOptions{}); !errors.Is(err, ErrEnvConflict) {
		t.Fatalf("recovery accepted changed declaration: %v", err)
	}
	recovered, err := next.Ensure(t.Context(), b.Name)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.ID != b.ID || !maps.Equal(recovered.Spec().Env, want) {
		t.Fatal("recovery lost Bed identity or environment")
	}
	env, err := next.buildExecutionEnv(recovered, nil)
	if err != nil || envMap(env)["VALUE"] != want["VALUE"] {
		t.Fatalf("recovered execution environment: %v", err)
	}
	if ok, err := next.Evict(t.Context(), b.Name); err != nil || !ok {
		t.Fatalf("evict: %v %v", ok, err)
	}
	fresh, err := next.Ensure(t.Context(), b.Name)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ID == b.ID || len(fresh.Spec().Env) != 0 {
		t.Fatal("forgotten environment survived a new local Bed lifetime")
	}
}

func TestServiceDoesNotInheritBedExecutionEnv(t *testing.T) {
	m := newTestManager(t)
	defer m.Close(context.Background())
	m.SetCarrierEnvironment([]string{"PATH=/usr/bin:/bin", "VALUE=carrier"})
	if _, err := m.InitializeBedWithOptions(t.Context(), "service-env", CreateOptions{Env: map[string]string{"BASE_ONLY": "private", "VALUE": "base"}}); err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(t.Context(), "service-env")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{{nil, "carrier//service-env"}, {map[string]string{"VALUE": "service"}, "service//service-env"}} {
		process, err := (serviceRuntime{manager: m, bed: b}).Start(t.Context(), service.Launch{Command: []string{"/bin/sh", "-c", `printf '%s/%s/%s' "$VALUE" "$BASE_ONLY" "$BED_ID"`}, Directory: "/workspace", Env: tc.env})
		if err != nil {
			t.Fatal(err)
		}
		execution := process.(*serviceProcess).execution
		if result := execution.Wait(); result.Process.Kind != executor.ProcessExited || result.Process.ExitCode != 0 {
			t.Fatalf("service result: %+v", result)
		}
		output, _, _, _ := execution.Logs(-1)
		if executionOutputText(output) != tc.want {
			t.Fatalf("service environment: %q, want %q", executionOutputText(output), tc.want)
		}
	}
}

func TestCreateBedRejectsInvalidEnvBeforeAdmission(t *testing.T) {
	m := newTestManager(t)
	defer m.Close(context.Background())
	for _, env := range []map[string]string{{"HOSTEL_SECRET": "x"}, {"BED_ID": "x"}, {"PLAYWRIGHT_MCP_CDP_ENDPOINT": "x"}, {"bad-name": "x"}, {"VALUE": "x\x00y"}} {
		if _, err := m.InitializeBedWithOptions(t.Context(), "invalid-env", CreateOptions{Env: env}); !errors.Is(err, ErrInvalidEnvironment) {
			t.Fatalf("invalid environment accepted: %v", err)
		}
	}
	if _, exists := m.Initialization("invalid-env"); exists {
		t.Fatal("invalid environment reserved a Bed")
	}
}

func TestBedEnvInFlightDeclarationIsImmutable(t *testing.T) {
	root := t.TempDir()
	backend := &initializationBlockingStore{fakeStore: newFakeStore(), started: make(chan struct{}, 1), release: make(chan struct{})}
	host := hostfacts.Collect()
	m, err := NewManager(host, root, "default", "/bin/bash", isolation.New(host, "shared", root), nil, 1, store.NewManagerWithStores(backend))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(context.Background())
	defer close(backend.release)
	declared := map[string]string{"VALUE": "initial"}
	if _, err := m.InitializeBedWithOptions(t.Context(), "pending", CreateOptions{Env: declared}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("initialization did not reach Store")
	}
	if _, err := m.InitializeBedWithOptions(t.Context(), "pending", CreateOptions{Env: declared}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.InitializeBedWithOptions(t.Context(), "pending", CreateOptions{Env: map[string]string{"VALUE": "different"}}); !errors.Is(err, ErrEnvConflict) {
		t.Fatalf("in-flight declaration changed: %v", err)
	}
	if m.OccupiedBedCount() != 1 {
		t.Fatal("duplicate create reserved another Bed")
	}
}
