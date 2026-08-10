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
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/executor"
)

func TestExecutionOutputRetentionIsBoundedWithoutTruncatingLiveOutput(t *testing.T) {
	execution := newExecution(context.Background(), "bed-output", ExecutionForeground, "executor-test", "local", nil)
	text := strings.Repeat("x", executionOutputBytes+10)

	live := execution.appendOutput(StreamStdout, text)
	if live.Text != text {
		t.Fatalf("live output length = %d, want %d", len(live.Text), len(text))
	}
	output, next, running, truncated := execution.Logs(-1)
	if len(output) != 1 || len(output[0].Text) != executionOutputBytes {
		t.Fatalf("retained output = %+v", output)
	}
	if next != 0 || !running || !truncated {
		t.Fatalf("logs metadata: next=%d running=%v truncated=%v", next, running, truncated)
	}
}

func TestExecutionCompletionRejectsLateStop(t *testing.T) {
	var stopped atomic.Bool
	execution := newExecution(context.Background(), "bed-finish", ExecutionSession, "executor-test", "local", func() {
		stopped.Store(true)
	})
	cause, stopDone := execution.claimFinish()
	if cause != "" || stopDone != nil {
		t.Fatalf("claimFinish = %q, %v", cause, stopDone)
	}
	if execution.RequestStop(CauseTimeout) {
		t.Fatal("late timeout was accepted after process completion")
	}
	if stopped.Load() {
		t.Fatal("late timeout invoked the session stop function")
	}
}

func TestExecutionPreservesExternalSignal(t *testing.T) {
	m := newTestManager(t)
	b, _ := m.Ensure(context.Background(), "signal")

	result, err := m.RunForeground(context.Background(), b, "kill -TERM $$", "", nil, 0, nil)
	if err != nil {
		t.Fatalf("RunForeground: %v", err)
	}
	if result.Process.Kind != executor.ProcessSignaled || result.Process.Signal != int(syscall.SIGTERM) ||
		result.Cause != CauseExternalSignal {
		t.Fatalf("signal result = %+v", result)
	}
}

func TestExecutionRecordsTimeoutBeforeKill(t *testing.T) {
	m := newTestManager(t)
	b, _ := m.Ensure(context.Background(), "timeout")

	result, err := m.RunForeground(context.Background(), b, "sleep 30", "", nil, 30*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("RunForeground: %v", err)
	}
	if result.Process.Kind != executor.ProcessSignaled || result.Process.Signal != int(syscall.SIGKILL) ||
		result.Cause != CauseTimeout {
		t.Fatalf("timeout result = %+v", result)
	}
}

func TestExecutionRecordsClientCancellationBeforeKill(t *testing.T) {
	m := newTestManager(t)
	b, _ := m.Ensure(context.Background(), "cancel")
	ctx, cancel := context.WithCancel(context.Background())

	result, err := m.RunForeground(ctx, b, "echo ready; sleep 30", "", nil, 0, func(ExecutionOutput) {
		cancel()
	})
	if err != nil {
		t.Fatalf("RunForeground: %v", err)
	}
	if result.Process.Kind != executor.ProcessSignaled || result.Process.Signal != int(syscall.SIGKILL) ||
		result.Cause != CauseClientCanceled {
		t.Fatalf("cancellation result = %+v", result)
	}
}

func TestBackgroundInterruptHasStructuredCause(t *testing.T) {
	m := newTestManager(t)
	b, _ := m.Ensure(context.Background(), "interrupt")
	started := make(chan struct{})
	var once sync.Once

	execution, err := m.StartExecution(nil, b, ExecutionBackground, "echo ready; sleep 30", "", nil, 0, nil, func(ExecutionOutput) {
		once.Do(func() { close(started) })
	})
	if err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("background execution did not start")
	}
	if !execution.RequestStop(CauseInterrupted) {
		t.Fatal("interrupt was not accepted")
	}
	result := execution.Wait()
	if result.Process.Kind != executor.ProcessSignaled || result.Process.Signal != int(syscall.SIGKILL) ||
		result.Cause != CauseInterrupted {
		t.Fatalf("interrupt result = %+v", result)
	}
}
