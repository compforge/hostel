//go:build linux

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

package executor

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/resource"
	"github.com/qiankunli/hostel/internal/supervisor"
)

// TestMain lets the package test binary serve as the re-exec target used by
// SupervisorFactory, exactly as cmd/hostel does in production.
func TestMain(m *testing.M) {
	if len(os.Args) >= 2 && os.Args[1] == supervisor.Arg {
		os.Exit(supervisor.Run(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func TestSupervisorStartIsIdempotentAndTerminalStatusReconnects(t *testing.T) {
	factory, err := NewSupervisorFactory(os.Args[0], resource.Noop("test"))
	if err != nil {
		t.Fatal(err)
	}
	defer factory.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bedExecutor, err := factory.Create(ctx, "bed-idempotent")
	if err != nil {
		t.Fatal(err)
	}
	defer bedExecutor.Shutdown(ctx)

	cmd, output := testCommand(t, "printf once")
	process, err := bedExecutor.Start(ctx, "process-stable", cmd)
	if err != nil {
		t.Fatal(err)
	}
	closeCommandOutput(t, cmd)
	outcome, err := process.Wait(ctx)
	if err != nil || outcome.Kind != ProcessExited || outcome.ExitCode != 0 {
		t.Fatalf("first wait: outcome=%+v err=%v", outcome, err)
	}
	if got := readOutput(t, output); got != "once" {
		t.Fatalf("output = %q", got)
	}

	// Start uses a fresh connection. Repeating the same identity and spec must
	// return the retained process, not fork a second command.
	retryCmd, retryOutput := testCommand(t, "printf once")
	retried, err := bedExecutor.Start(ctx, "process-stable", retryCmd)
	if err != nil {
		t.Fatal(err)
	}
	closeCommandOutput(t, retryCmd)
	_ = retryOutput.Close()
	if retried.PID() != process.PID() {
		t.Fatalf("retry pid = %d, want retained pid %d", retried.PID(), process.PID())
	}
	if retriedOutcome, err := retried.Wait(ctx); err != nil || retriedOutcome != outcome {
		t.Fatalf("retry wait: outcome=%+v err=%v, want %+v", retriedOutcome, err, outcome)
	}

	different, differentOutput := testCommand(t, "printf different")
	defer differentOutput.Close()
	if _, err := bedExecutor.Start(ctx, "process-stable", different); err == nil || !strings.Contains(err.Error(), "different specification") {
		t.Fatalf("process id reuse error = %v", err)
	}
	closeCommandOutput(t, different)

	concrete := bedExecutor.(*supervisedExecutor)
	if err := supervisor.NewClient(concrete.socket, "executor-stale").Describe(); err == nil || !strings.Contains(err.Error(), "executor mismatch") {
		t.Fatalf("stale executor fencing error = %v", err)
	}
}

func TestSupervisorLossIsStructuredAndReplacementGetsNewIdentity(t *testing.T) {
	factory, err := NewSupervisorFactory(os.Args[0], resource.Noop("test"))
	if err != nil {
		t.Fatal(err)
	}
	defer factory.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bedExecutor, err := factory.Create(ctx, "bed-replace")
	if err != nil {
		t.Fatal(err)
	}
	cmd, output := testCommand(t, "sleep 30")
	defer output.Close()
	process, err := bedExecutor.Start(ctx, "process-lost", cmd)
	if err != nil {
		t.Fatal(err)
	}
	closeCommandOutput(t, cmd)
	concrete := bedExecutor.(*supervisedExecutor)
	if err := concrete.proc.Kill(); err != nil {
		t.Fatal(err)
	}
	outcome, err := process.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != ProcessLost || outcome.Error != "executor "+bedExecutor.ID()+" lost" {
		t.Fatalf("lost outcome = %+v", outcome)
	}
	if outcome.Detail == "" {
		t.Fatal("lost outcome omitted server-side detail")
	}

	replacement, err := factory.Create(ctx, "bed-replace")
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Shutdown(ctx)
	if replacement.ID() == bedExecutor.ID() {
		t.Fatalf("replacement reused executor identity %q", replacement.ID())
	}
}

func testCommand(t *testing.T, script string) (*exec.Cmd, *os.File) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devnull.Close() })
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = os.Environ()
	cmd.Stdin = devnull
	cmd.Stdout = write
	cmd.Stderr = write
	return cmd, read
}

func readOutput(t *testing.T, file *os.File) string {
	t.Helper()
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func closeCommandOutput(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := cmd.Stdout.(*os.File).Close(); err != nil {
		t.Fatal(err)
	}
}
