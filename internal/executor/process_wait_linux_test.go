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
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestWaitCommandBeforeReapKeepsPIDReservedDuringCallback(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid

	barrierEntered := make(chan struct{})
	releaseBarrier := make(chan struct{})
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- waitCommandBeforeReap(cmd, func(err error) error {
			if err != nil {
				t.Errorf("exit barrier: %v", err)
			}
			close(barrierEntered)
			<-releaseBarrier
			return nil
		})
	}()

	select {
	case <-barrierEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for exit barrier")
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err != nil {
		t.Fatalf("pid was reaped before exit barrier callback: %v", err)
	}

	close(releaseBarrier)
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out reaping process after exit barrier")
	}
}

func TestWaitCommandBarrierFailureKillsBeforePublishingExit(t *testing.T) {
	originalWaitid := waitid
	waitid = func(int, int, *unix.Siginfo, int, *unix.Rusage) error {
		return syscall.EINVAL
	}
	defer func() { waitid = originalWaitid }()

	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	proc := &localProcess{cmd: cmd, pid: cmd.Process.Pid, executorID: "executor-test", done: make(chan struct{})}

	proc.reap()
	outcome, err := proc.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != ProcessLost || !strings.Contains(outcome.Detail, syscall.EINVAL.Error()) {
		t.Fatalf("Wait = %+v, want exit barrier error", outcome)
	}
	if !proc.exited {
		t.Fatal("process was not marked exited after forced termination")
	}
	if cmd.ProcessState == nil {
		t.Fatal("process was not reaped after forced termination")
	}
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("process status = %v, want SIGKILL", cmd.ProcessState)
	}
}
