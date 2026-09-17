package manager

import (
	"context"
	"fmt"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestBackgroundHistoryOutlivesBoundedHotOutput(t *testing.T) {
	m := newTestManager(t)
	defer m.Close(context.Background())
	b, err := m.Ensure(t.Context(), "default")
	if err != nil {
		t.Fatal(err)
	}
	e, err := m.StartExecution(t.Context(), b, ExecutionBackground, "printf retained", "", "", nil, time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.Wait()
	r := m.Executions()
	// Real output is independently readable even after newer completed commands
	// exhaust the native hot fragment cache. No extra processes are needed here.
	r.mu.Lock()
	for i := 0; i < executionHistoryLimit+1; i++ {
		id := fmt.Sprintf("background-%d", i)
		end := time.Now()
		record := &Execution{ID: id, Mode: ExecutionBackground, result: &ExecutionResult{}, finishedAt: &end}
		record.appendOutput(StreamStdout, strings.Repeat("x", executionOutputBytes))
		r.executions[id] = record
		r.order = append(r.order, id)
	}
	r.mu.Unlock()
	r.prune()
	bytes, records := 0, 0
	r.mu.Lock()
	for _, record := range r.executions {
		bytes += record.outputBytes
		records++
	}
	r.mu.Unlock()
	if bytes > executionHistoryLimit*executionOutputBytes || records < executionHistoryLimit+2 {
		t.Fatal(bytes, records)
	}
	fragments, cursor, running, truncated := e.Logs(-1)
	if len(fragments) != 0 || cursor != 0 || running || !truncated {
		t.Fatal(fragments, cursor, running, truncated)
	}
	f, end, err := e.OpenBackgroundLog(0)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil || string(data) != "retained" || end != 8 {
		t.Fatal(string(data), end, err)
	}
	// Background metadata must not crowd a newly completed native command out.
	foreground, err := m.StartExecution(t.Context(), b, ExecutionForeground, "printf current", "", "", nil, time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	foreground.Wait()
	r.prune()
	if _, ok := r.Get(foreground.ID); !ok {
		t.Fatal("background metadata evicted new foreground history")
	}
}

func TestRestartCleansUnrecoverableExecutionLogs(t *testing.T) {
	m := newTestManager(t)
	b, err := m.Ensure(t.Context(), "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.BedFS().Write("keep", []byte("user data"), 0600); err != nil {
		t.Fatal(err)
	}
	e, err := m.StartExecution(t.Context(), b, ExecutionBackground, "printf old", "", "", nil, time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.Wait()
	oldLog, root := e.log.path, m.root
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	host := hostfacts.Collect()
	m2, err := NewManager(host, root, "default", "/bin/bash", isolation.New(host, "shared", root), nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close(context.Background())
	restored, err := m2.Ensure(t.Context(), "default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldLog); !os.IsNotExist(err) {
		t.Fatal("unrecoverable log remains", err)
	}
	if _, ok := m2.Executions().Get(e.ID); ok {
		t.Fatal("history unexpectedly recovered")
	}
	if _, err := restored.BedFS().Stat("keep"); err != nil {
		t.Fatal("cleanup touched BedFS", err)
	}
	fresh, err := m2.StartExecution(t.Context(), restored, ExecutionBackground, "printf fresh", "", "", nil, time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Wait()
	if err := m2.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fresh.log.path); err != nil {
		t.Fatal("repeated Start removed current logs", err)
	}
}
