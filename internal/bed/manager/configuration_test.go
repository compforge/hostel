package manager

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/configuration"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

func TestConfigurationLifecycleAndProcessConsumers(t *testing.T) {
	root, source := t.TempDir(), t.TempDir()
	for key, value := range map[string]string{"MAIN_VALUE": "main-private", "SERVICE_VALUE": "service-private"} {
		if err := os.WriteFile(filepath.Join(source, key), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	host := hostfacts.Collect()
	m, err := NewManager(host, root, "default", "/bin/bash", isolation.New(host, "shared", root), nil, 0, nil,
		WithConfiguration(configuration.Config{Sources: map[string]string{"runtime": source, "unused": filepath.Join(source, "missing")}}))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(context.Background())
	request := CreateOptions{
		EnvValueFrom: map[string]model.ConfigurationKeyRef{"MAIN_VALUE": {Source: "runtime", Key: "MAIN_VALUE"}},
		Services: []model.ServiceSpec{{Name: "worker", Command: []string{"/bin/sh", "-c", "test -z \"$MAIN_VALUE\" && printf '%s' \"$SERVICE_VALUE\" > service-config; exec sleep 60"},
			EnvValueFrom: map[string]model.ConfigurationKeyRef{"SERVICE_VALUE": {Source: "runtime", Key: "SERVICE_VALUE"}}, Required: true}},
	}
	if _, err := m.InitializeBedWithOptions(t.Context(), "configured", request); err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(t.Context(), "configured")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, err := os.ReadFile(filepath.Join(b.Workdir(), "service-config"))
		if err == nil && string(data) == "service-private" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("service did not receive independent config: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var out strings.Builder
	result, err := m.RunForeground(t.Context(), b, "printf '%s/%s' \"$MAIN_VALUE\" \"$SERVICE_VALUE\"", "", nil, time.Second, func(o ExecutionOutput) { out.WriteString(o.Text) })
	if err != nil || result.Process.ExitCode != 0 || out.String() != "main-private/" {
		t.Fatalf("main configuration: %q %v", out.String(), err)
	}
	other, err := m.Ensure(t.Context(), "plain")
	if err != nil {
		t.Fatal(err)
	}
	env, err := m.buildExecutionEnv(other, nil)
	if err != nil || envMap(env)["MAIN_VALUE"] != "" || envMap(env)["SERVICE_VALUE"] != "" {
		t.Fatal("configuration leaked to plain Bed")
	}
	if _, err := m.InitializeBedWithOptions(t.Context(), "configured", CreateOptions{Services: request.Services}); !errors.Is(err, ErrEnvConflict) {
		t.Fatalf("source declaration mutation: %v", err)
	}
	data, err := os.ReadFile(m.identityPath("configured"))
	if err != nil {
		t.Fatal(err)
	}
	status, _ := json.Marshal(m.Status())
	for _, raw := range []string{string(data), string(status)} {
		if strings.Contains(raw, "main-private") || strings.Contains(raw, "service-private") {
			t.Fatal("resolved values persisted or reported")
		}
	}
	if ok, err := m.Evict(t.Context(), "configured"); err != nil || !ok {
		t.Fatalf("eviction: %v", err)
	}
	if _, err := m.configurations.Main(b.Bed); err == nil {
		t.Fatal("eviction retained configuration")
	}
}
