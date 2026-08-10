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

package bed

import (
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bedinit"
)

// TestMain lets this test binary double as the __bedinit re-exec target (the
// real hostel binary dispatches the same way in cmd/hostel/main.go), so
// EnableBedInit(os.Args[0]) exercises the genuine fork path.
func TestMain(m *testing.M) {
	if len(os.Args) >= 2 && os.Args[1] == bedinit.InitArg {
		os.Exit(bedinit.Run(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func newBedInitManager(t *testing.T) *Manager {
	t.Helper()
	m := newTestManager(t)
	if err := m.EnableBedInit(os.Args[0]); err != nil {
		t.Fatalf("EnableBedInit: %v", err)
	}
	return m
}

// resolveBedInit resolves a bed and guarantees its init (which inherits the
// test binary's stderr) is torn down with the test — Pdeathsig covers crashes,
// but orderly cleanup keeps `go test` from waiting on the inherited fd.
func resolveBedInit(t *testing.T, m *Manager, id string) *Bed {
	t.Helper()
	b, err := m.Ensure(id)
	if err != nil {
		t.Fatalf("Resolve(%s): %v", id, err)
	}
	t.Cleanup(func() { _, _ = m.Evict(id) })
	return b
}

// TestBedInitForegroundExec runs the full wired path: Manager → initSpawner →
// bedinit → command. Exit codes, output streaming, env and set -e isolation
// must behave exactly like the in-process spawner.
func TestBedInitForegroundExec(t *testing.T) {
	m := newBedInitManager(t)
	b := resolveBedInit(t, m, "conv-init")
	ctx := context.Background()

	var out strings.Builder
	result, err := m.RunForeground(ctx, b, "echo via-init; echo v=$BEDINIT_T", "", map[string]string{"BEDINIT_T": "42"}, 0, func(output ExecutionOutput) { out.WriteString(output.Text) })
	if err != nil || result.Process.ExitCode != 0 {
		t.Fatalf("RunForeground: result=%+v err=%v", result, err)
	}
	if !strings.Contains(out.String(), "via-init") || !strings.Contains(out.String(), "v=42") {
		t.Fatalf("output = %q", out.String())
	}

	// set -e failure stays isolated; the bed remains usable (execd parity).
	if result, err := m.RunForeground(ctx, b, "set -euo pipefail\nfalse", "", nil, 0, nil); err != nil || result.Process.ExitCode == 0 {
		t.Fatalf("set -e run: result=%+v err=%v", result, err)
	}
	if result, err := m.RunForeground(ctx, b, "true", "", nil, 0, nil); err != nil || result.Process.ExitCode != 0 {
		t.Fatalf("bed unusable after failure: result=%+v err=%v", result, err)
	}
}

// TestBedInitTeardownKillsTree is S1's payoff: evicting the bed terminates its
// init, which must take down an in-flight command AND a setsid escapee that no
// pgid sweep could reach.
func TestBedInitTeardownKillsTree(t *testing.T) {
	m := newBedInitManager(t)
	b := resolveBedInit(t, m, "conv-init-kill")

	pidfile := t.TempDir() + "/escapee.pid"
	done := make(chan ExecutionResult, 1)
	go func() {
		result, _ := m.RunForeground(context.Background(), b,
			"setsid sh -c 'echo $$ > "+pidfile+"; sleep 60' & sleep 60", "", nil, 0, nil)
		done <- result
	}()
	var escapee int
	waitForCond(t, "escapee pidfile", func() bool {
		bts, err := os.ReadFile(pidfile)
		if err != nil {
			return false
		}
		fields := strings.Fields(string(bts))
		if len(fields) == 0 {
			return false
		}
		n := 0
		for _, c := range fields[0] {
			n = n*10 + int(c-'0')
		}
		escapee = n
		return escapee > 0
	})

	if ok, err := m.Evict("conv-init-kill"); err != nil || !ok {
		t.Fatalf("Evict: ok=%v err=%v", ok, err)
	}
	select {
	case result := <-done:
		if result.Process.Kind != ProcessSignaled || result.Cause != CauseBedTeardown {
			t.Fatalf("killed command result = %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight command escaped bed teardown")
	}
	waitForCond(t, "setsid escapee killed", func() bool {
		return syscall.Kill(escapee, 0) != nil
	})
}

// TestBedInitSessionShell: the /session persistent shell also lives under the
// bed's init and keeps its stateful semantics.
func TestBedInitSessionShell(t *testing.T) {
	m := newBedInitManager(t)
	b := resolveBedInit(t, m, "conv-init-shell")

	sh, err := m.ForegroundShell(b)
	if err != nil {
		t.Fatalf("ForegroundShell: %v", err)
	}
	ctx := context.Background()
	if _, err := sh.Run(ctx, "export INIT_SHELL=yes", nil); err != nil {
		t.Fatalf("Run export: %v", err)
	}
	var out strings.Builder
	res, err := sh.Run(ctx, "echo got=$INIT_SHELL", func(l string) { out.WriteString(l) })
	if err != nil || res.ExitCode != 0 || !strings.Contains(out.String(), "got=yes") {
		t.Fatalf("state lost: res=%+v err=%v out=%q", res, err, out.String())
	}
}

func waitForCond(t *testing.T, what string, ok func() bool) {
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
