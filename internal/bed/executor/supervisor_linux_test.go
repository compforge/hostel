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
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	hostel "github.com/qiankunli/hostel/internal"
	"github.com/qiankunli/hostel/internal/bed/executor/supervisor"
	"github.com/qiankunli/hostel/internal/bed/resource"
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

func TestRejectedLargeStartsKeepServiceAndExecutor(t *testing.T) {
	factory, err := NewSupervisorFactory(os.Args[0], resource.Noop("test"))
	if err != nil {
		t.Fatal(err)
	}
	defer factory.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bedExecutor, err := factory.Create(ctx, "bed-large-start")
	if err != nil {
		t.Fatal(err)
	}
	defer bedExecutor.Shutdown(ctx)
	serviceCmd, serviceOutput := testCommand(t, "sleep 30")
	service, err := bedExecutor.(*supervisedExecutor).StartService(ctx, "service-stays-up", serviceCmd)
	if err != nil {
		t.Fatal(err)
	}
	closeCommandOutput(t, serviceCmd)
	defer serviceOutput.Close()

	for _, tc := range []struct {
		name    string
		command string
		local   bool
	}{
		{name: "execve E2BIG", command: strings.Repeat("x", 150<<10)},
		{name: "specification cap", command: strings.Repeat("x", 4<<20), local: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, output := testCommand(t, tc.command)
			defer output.Close()
			_, err := bedExecutor.Start(ctx, "process-"+tc.name, cmd)
			closeCommandOutput(t, cmd)
			if err == nil {
				t.Fatal("oversized Start succeeded")
			}
			if !errors.Is(err, hostel.ErrLimitExceeded) {
				t.Fatalf("Start error = %v, want limit exceeded", err)
			}
			var requestErr *supervisor.RequestError
			if tc.local != errors.As(err, &requestErr) {
				t.Fatalf("Start error = %v, local rejection = %t", err, tc.local)
			}
			if bedExecutor.State() != StateReady {
				t.Fatalf("Executor state after rejected Start = %s", bedExecutor.State())
			}
			if err := syscall.Kill(service.PID(), 0); err != nil {
				t.Fatalf("service lost after rejected Start: %v", err)
			}
		})
	}

	cmd, output := testCommand(t, "printf healthy")
	process, err := bedExecutor.Start(ctx, "process-after-rejection", cmd)
	if err != nil {
		t.Fatal(err)
	}
	closeCommandOutput(t, cmd)
	if status, err := process.Wait(ctx); err != nil || status.Kind != ProcessExited || status.ExitCode != 0 {
		t.Fatalf("next execution = %+v, err = %v", status, err)
	}
	if got := readOutput(t, output); got != "healthy" {
		t.Fatalf("next execution output = %q", got)
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

func TestSupervisorExtraFilesReachChildAndRejectChangedRetry(t *testing.T) {
	factory, err := NewSupervisorFactory(os.Args[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer factory.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ex, err := factory.Create(ctx, "files")
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Shutdown(ctx)
	file, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString("inherited-file"); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	cmd, out := testCommand(t, "cat <&3")
	// Force specification offloading alongside inherited setup descriptors.
	cmd.Env = append(cmd.Env, "LARGE_A="+strings.Repeat("x", 70000), "LARGE_B="+strings.Repeat("y", 70000))
	cmd.ExtraFiles = []*os.File{file}
	p, err := ex.Start(ctx, "fd-command", cmd)
	if err != nil {
		t.Fatal(err)
	}
	closeCommandOutput(t, cmd)
	result, err := p.Wait(ctx)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if got := readOutput(t, out); got != "inherited-file" {
		t.Fatalf("output=%q", got)
	}
	other, err := os.CreateTemp(t.TempDir(), "different")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	retry, retryOut := testCommand(t, "cat <&3")
	defer retryOut.Close()
	defer closeCommandOutput(t, retry)
	retry.Env = cmd.Env
	retry.ExtraFiles = []*os.File{other}
	if _, err := ex.Start(ctx, "fd-command", retry); err == nil {
		t.Fatal("changed extra descriptor accepted on retry")
	}
	if ex.State() != StateReady {
		t.Fatal("semantic rejection replaced Executor")
	}
}

func TestPrivatePIDNamespaceSupervisor(t *testing.T) {
	if os.Getenv("HOSTEL_TEST_PID_NAMESPACE") != "1" {
		t.Skip("set HOSTEL_TEST_PID_NAMESPACE=1 on a Linux runner with namespace privileges")
	}
	factory, err := NewSupervisorFactory(os.Args[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	factory.privatePIDNamespace = true
	defer factory.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ex, err := factory.Create(ctx, "private-processes")
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Shutdown(ctx)
	cmd, out := testCommand(t, `test "$(readlink /proc/1/ns/pid)" = "$(readlink /proc/self/ns/pid)" && test "$(awk '/^NSpid:/ { print NF }' /proc/self/status)" = 2 && printf private-procfs`)
	p, err := ex.Start(ctx, "procfs", cmd)
	if err != nil {
		t.Fatal(err)
	}
	closeCommandOutput(t, cmd)
	result, err := p.Wait(ctx)
	if err != nil || result.Kind != ProcessExited || result.ExitCode != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if got := readOutput(t, out); got != "private-procfs" {
		t.Fatalf("output=%q", got)
	}
	long, longOut := testCommand(t, "exec sleep 60")
	long.Path, long.Args = "/bin/sleep", []string{"sleep", "60"}
	defer longOut.Close()
	process, err := ex.Start(ctx, "long-running", long)
	if err != nil {
		t.Fatal(err)
	}
	closeCommandOutput(t, long)
	if err := syscall.Kill(process.PID(), 0); err != nil {
		t.Fatalf("PID is not caller-visible: %d %v", process.PID(), err)
	}
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", process.PID()))
	if err != nil || string(cmdline) != "/bin/sleep\x0060\x00" {
		t.Fatalf("caller-visible PID identifies wrong process: %q %v", cmdline, err)
	}
	process.Kill()
	result, err = process.Wait(ctx)
	if err != nil || result.Kind != ProcessSignaled {
		t.Fatalf("kill result=%+v err=%v", result, err)
	}
}
