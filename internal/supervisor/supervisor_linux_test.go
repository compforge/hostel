// Copyright 2026 Li Qiankun
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package supervisor

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestMain doubles as the supervisor helper process (stdlib helper idiom): the
// subreaper semantics under test require a REAL separate process — in-process
// Serve would make children of the test binary and its wait4(-1) loop would
// race os/exec used elsewhere.
func TestMain(m *testing.M) {
	if os.Getenv("SUPERVISOR_HELPER") == "1" {
		os.Exit(Run([]string{
			"--socket", os.Getenv("SUPERVISOR_SOCKET"),
			"--bed", "test",
			"--executor", testExecutorID,
		}))
	}
	os.Exit(m.Run())
}

// startSupervisor launches the helper supervisor and waits for its socket.
const testExecutorID = "executor-test"

func startSupervisor(t *testing.T) (socket string, proc *os.Process, client *Client) {
	t.Helper()
	socket = filepath.Join(t.TempDir(), "init.sock")
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "SUPERVISOR_HELPER=1", "SUPERVISOR_SOCKET="+socket)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	for i := 0; i < 100; i++ {
		client = NewClient(socket, testExecutorID)
		if err := client.Describe(); err == nil {
			return socket, cmd.Process, client
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("supervisor socket never appeared")
	return "", nil, nil
}

var nextProcessID atomic.Uint64

// spawnSh starts `sh -c script` through a fresh RPC and returns its stable
// process identity, pid and combined output.
func spawnSh(t *testing.T, client *Client, script string) (string, int, *os.File) {
	t.Helper()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	processID := fmt.Sprintf("process-%d", nextProcessID.Add(1))
	pid, err := client.Start(processID, []string{"/bin/sh", "-c", script}, "", os.Environ(), devnull, pw, pw)
	pw.Close()
	if err != nil {
		pr.Close()
		t.Fatalf("Start: %v", err)
	}
	return processID, pid, pr
}

func TestSpawnExitCodeAndOutput(t *testing.T) {
	_, _, client := startSupervisor(t)

	processID, _, out := spawnSh(t, client, "echo hi from supervisor; exit 7")
	data, _ := io.ReadAll(out)
	out.Close()
	status, err := client.Wait(processID)
	if err != nil {
		t.Fatalf("WaitExit: %v", err)
	}
	if status.Kind != "exited" || status.ExitCode != 7 {
		t.Fatalf("exit = %+v, want code 7", status)
	}
	if !strings.Contains(string(data), "hi from supervisor") {
		t.Fatalf("output = %q", data)
	}
}

func TestKillSpawnedProcess(t *testing.T) {
	_, _, client := startSupervisor(t)

	processID, _, out := spawnSh(t, client, "sleep 60")
	defer out.Close()
	if err := client.Kill(processID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	status, err := client.Wait(processID)
	if err != nil {
		t.Fatalf("WaitExit: %v", err)
	}
	if status.Kind != "signaled" || status.Signal != int(syscall.SIGKILL) {
		t.Fatalf("exit = %+v, want SIGKILL", status)
	}
}

func TestConcurrentShortLivedSpawns(t *testing.T) {
	_, _, client := startSupervisor(t)
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()

	const count = 128
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			processID := fmt.Sprintf("process-concurrent-%d", nextProcessID.Add(1))
			_, err := client.Start(processID, []string{"/bin/true"}, "", os.Environ(), devnull, devnull, devnull)
			if err != nil {
				errs <- err
				return
			}
			status, err := client.Wait(processID)
			if err != nil {
				errs <- err
				return
			}
			if status.Kind != "exited" || status.ExitCode != 0 {
				errs <- fmt.Errorf("exit = %+v, want code 0", status)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestParentageAndSigtermKillsTree is the point of the supervisor: the spawned child
// is a child of the INIT (not of this process), and SIGTERM to the init takes
// the whole tree down — including a setsid daemon that escaped its pgid.
func TestParentageAndSigtermKillsTree(t *testing.T) {
	_, initProc, client := startSupervisor(t)

	// Long runner + a setsid-style escapee writing its pid.
	pidfile := filepath.Join(t.TempDir(), "escapee.pid")
	_, pid, out := spawnSh(t, client,
		"setsid sh -c 'echo $$ > "+pidfile+"; sleep 60' & echo started; sleep 60")
	go io.Copy(io.Discard, out) //nolint:errcheck // drain so the child never blocks
	defer out.Close()

	// Parentage: the child's ppid must be the init, not us.
	waitFor(t, "child parented to init", func() bool {
		stat, err := os.ReadFile("/proc/" + itoa(pid) + "/stat")
		return err == nil && strings.Contains(string(stat), ") S "+itoa(initProc.Pid)+" ")
	})
	var escapee int
	waitFor(t, "escapee pidfile", func() bool {
		b, err := os.ReadFile(pidfile)
		if err != nil {
			return false
		}
		escapee = atoi(strings.TrimSpace(string(b)))
		return escapee > 0
	})

	if err := initProc.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM init: %v", err)
	}
	waitFor(t, "direct child killed", func() bool { return !alive(pid) })
	waitFor(t, "setsid escapee killed", func() bool { return !alive(escapee) })
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func alive(pid int) bool {
	// Signal 0 probes existence; a zombie still "exists" but its stat state is
	// Z and it is gone for our purposes once the supervisor reaped it —
	// after the supervisor exits, reparenting to real init reaps promptly.
	err := syscall.Kill(pid, 0)
	if err != nil {
		return false
	}
	stat, rerr := os.ReadFile("/proc/" + itoa(pid) + "/stat")
	if rerr != nil {
		return false
	}
	return !strings.Contains(string(stat), ") Z ")
}

func itoa(i int) string { return strconv.Itoa(i) }

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
