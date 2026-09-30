//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/tool"
)

// The fault is scoped to the supervisor identified by this run's API-issued
// Executor ID. The target fixture owns daemon and Bed cleanup even on failure.
func TestPrivatePIDNamespaceRecovery(t *testing.T) {
	if os.Getenv("HOSTEL_E2E_REQUIRE_PID_NAMESPACE") != "1" {
		t.Skip("requires privileged Linux PID namespace runner")
	}
	requireTestBinary(t)
	if runtime.GOOS != "linux" {
		t.Fatal("requires Linux")
	}
	options := restrictedOptions()
	options.Bed.Executor.PIDNS = value(tool.Required)
	options.Bed.Filesystem.Bwrap = value(tool.Required)
	c := startTarget(t, targetOptions{isolation: "suite", executor: "supervisor", config: &options}).client
	const bed = "process-recovery"
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	response, err := c.json(ctx, "POST", "/v1/beds", "", map[string]any{"id": bed}, nil)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "create recovery Bed", response)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		response, err := c.json(ctx, "DELETE", "/v1/beds/"+bed+"?purge=true", "", nil, nil)
		if err != nil {
			t.Error(err)
			return
		}
		must2xx(t, "purge recovery Bed", response)
	})
	first, response := c.command(t, bed, map[string]any{"command": "printf retained > /workspace/marker; readlink /proc/self/ns/pid; readlink /proc/self/ns/mnt", "timeout": 5000})
	must2xx(t, "first Executor", response)
	assertCommandExit(t, first, 0)
	firstView := strings.Fields(first.Stdout)
	if len(firstView) != 2 {
		t.Fatalf("missing view identity: %q", first.Stdout)
	}
	var before struct {
		Executor struct {
			ID string `json:"id"`
		} `json:"executor"`
	}
	ctx, cancel = context.WithTimeout(t.Context(), 5*time.Second)
	response, err = c.json(ctx, "GET", "/v1/beds/"+bed, "", nil, &before)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "read Executor identity", response)
	var session struct {
		ID string `json:"session_id"`
	}
	ctx, cancel = context.WithTimeout(t.Context(), 5*time.Second)
	response, err = c.json(ctx, "POST", "/session", bed, map[string]any{}, &session)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "create recovery session", response)
	result, response := c.stream(t, "/session/"+url.PathEscape(session.ID)+"/run", bed, map[string]any{"command": "sleep 300 >/dev/null 2>&1 & echo started", "timeout": 5000})
	must2xx(t, "create old descendants", response)
	assertCommandExit(t, result, 0)
	// Observe the actual namespace and supervisor identity before injecting loss.
	supervisorPID := 0
	var oldProcesses []string
	paths, err := filepath.Glob("/proc/[0-9]*/ns/pid")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		namespace, err := os.Readlink(path)
		if err != nil || namespace != firstView[0] {
			continue
		}
		process := filepath.Dir(filepath.Dir(path))
		oldProcesses = append(oldProcesses, process)
		cmdline, _ := os.ReadFile(filepath.Join(process, "cmdline"))
		if strings.Contains(string(cmdline), "\x00__supervisor\x00") && strings.Contains(string(cmdline), "\x00--executor\x00"+before.Executor.ID+"\x00") {
			supervisorPID, err = strconv.Atoi(filepath.Base(process))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if supervisorPID == 0 || len(oldProcesses) < 3 {
		t.Fatalf("fault target not established: supervisor=%d processes=%v", supervisorPID, oldProcesses)
	}
	// Keep the old namespace inode pinned so identity comparisons cannot pass or
	// fail merely because the kernel reuses an inode after reclamation.
	oldNS, err := os.Open(fmt.Sprintf("/proc/%d/ns/pid", supervisorPID))
	if err != nil {
		t.Fatal(err)
	}
	defer oldNS.Close()
	process, err := os.FindProcess(supervisorPID)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		alive := false
		for _, path := range oldProcesses {
			status, err := os.ReadFile(filepath.Join(path, "stat"))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			_, tail, ok := strings.Cut(string(status), ") ")
			if !ok || !strings.HasPrefix(tail, "Z ") {
				alive = true
			}
		}
		if !alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("old Executor left live descendants")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Poll the public lifecycle before issuing a new command, avoiding a race
	// between fault activation and daemon observation of the old supervisor exit.
	for {
		var detail struct {
			Executor struct {
				State string `json:"state"`
			} `json:"executor"`
		}
		ctx, cancel = context.WithTimeout(t.Context(), 5*time.Second)
		response, err = c.json(ctx, "GET", "/v1/beds/"+bed, "", nil, &detail)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "observe Executor loss", response)
		if detail.Executor.State == "lost" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("loss not observed: %+v", detail)
		}
		time.Sleep(20 * time.Millisecond)
	}
	second, response := c.command(t, bed, map[string]any{"command": "test \"$(cat /workspace/marker)\" = retained; readlink /proc/self/ns/pid; readlink /proc/self/ns/mnt", "timeout": 5000})
	must2xx(t, "replacement Executor", response)
	assertCommandExit(t, second, 0)
	secondView := strings.Fields(second.Stdout)
	if len(secondView) != 2 || secondView[0] == firstView[0] || second.Result.ExecutorID == before.Executor.ID {
		t.Fatalf("stale replacement: first=%q second=%+v", first.Stdout, second)
	}
	third, response := c.command(t, bed, map[string]any{"command": "readlink /proc/self/ns/pid; readlink /proc/self/ns/mnt", "timeout": 5000})
	must2xx(t, "reuse replacement view", response)
	assertCommandExit(t, third, 0)
	if third.Stdout != second.Stdout {
		t.Fatalf("Executor did not retain its view: %q != %q", third.Stdout, second.Stdout)
	}
	assertSuiteStatus(t, c, bed)
	t.Logf("Executor %s replaced by %s; %d old processes terminated; data retained", before.Executor.ID, second.Result.ExecutorID, len(oldProcesses))
}
