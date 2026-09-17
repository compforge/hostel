package manager

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackgroundLogRetentionAndBedLifetime(t *testing.T) {
	m := newTestManager(t)
	defer m.Close(context.Background())
	b, err := m.Ensure(t.Context(), "logs")
	if err != nil {
		t.Fatal(err)
	}
	e, err := m.StartExecution(t.Context(), b, ExecutionBackground, "printf retained", "", "", nil, time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.Wait()
	if err := m.executorManager.Current(b.Bed).Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RunForeground(t.Context(), b, "true", "", nil, time.Second, nil); err != nil {
		t.Fatal(err)
	}
	f, next, err := e.OpenBackgroundLog(0)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(f)
	f.Close()
	if string(data) != "retained" || next != 8 {
		t.Fatal(string(data), next)
	}
	// Size-based native history collection must not shorten background retention.
	registry := m.Executions()
	registry.mu.Lock()
	for i := 0; i < executionHistoryLimit+1; i++ {
		id := fmt.Sprintf("old-%d", i)
		finished := time.Now()
		registry.executions[id] = &Execution{ID: id, Mode: ExecutionForeground, result: &ExecutionResult{}, finishedAt: &finished}
		registry.order = append(registry.order, id)
	}
	registry.mu.Unlock()
	registry.prune()
	if _, ok := registry.Get(e.ID); !ok {
		t.Fatal("background history removed before retention deadline")
	}
	e.mu.Lock()
	expired := time.Now().Add(-25 * time.Hour)
	e.finishedAt = &expired
	e.mu.Unlock()
	registry.prune()
	if _, ok := registry.Get(e.ID); ok {
		t.Fatal("expired background retained")
	}
	if _, err := os.Stat(e.log.path); !os.IsNotExist(err) {
		t.Fatal("expired log not removed", err)
	}
	e, err = m.StartExecution(t.Context(), b, ExecutionBackground, "printf fresh", "", "", nil, time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.Wait()
	if err = m.Purge(t.Context(), b.Name); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get(e.ID); ok {
		t.Fatal("forgotten Bed retained execution history")
	}
	if _, err := os.Stat(e.log.path); !os.IsNotExist(err) {
		t.Fatal("purge retained log", err)
	}
}

func TestLogCleanupFailureIsRetried(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "not-removable")
	if err := os.Mkdir(logPath, 0700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(logPath, "child")
	if err := os.WriteFile(child, nil, 0600); err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-25 * time.Hour)
	e := &Execution{ID: "retry", Mode: ExecutionBackground, result: &ExecutionResult{}, finishedAt: &expired, log: &executionLog{path: logPath}}
	r := newExecutionRegistry()
	r.executions[e.ID] = e
	r.order = append(r.order, e.ID)
	r.prune()
	if len(r.order) != 1 {
		t.Fatal("failed cleanup lost its retry entry")
	}
	if err := os.Remove(child); err != nil {
		t.Fatal(err)
	}
	r.prune()
	if _, ok := r.Get(e.ID); ok {
		t.Fatal("successful cleanup retained execution")
	}
}
