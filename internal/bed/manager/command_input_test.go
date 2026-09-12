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

package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/executor"
)

func TestLocalCommandInput(t *testing.T) {
	m := newTestManager(t)
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	testCommandInput(t, m, "local")
}

func testCommandInput(t *testing.T, m *Manager, backend string) {
	t.Helper()
	large := strings.Repeat("中文\n'\"$HOME $(exit 8)\n", 65536)
	for _, tc := range []struct {
		name, command, input string
		mode                 ExecutionMode
		timeout              time.Duration
		cancelOnStart        bool
		cause                TerminationCause
		exit                 int
		file                 bool
	}{
		{name: "empty", command: "cat > result", mode: ExecutionForeground, cause: CauseNatural, file: true},
		{name: "large", command: "tee result", input: large, mode: ExecutionForeground, cause: CauseNatural, file: true},
		{name: "background", command: "cat > result", input: large, mode: ExecutionBackground, cause: CauseNatural, file: true},
		{name: "unread", command: "exit 7", input: large, mode: ExecutionForeground, cause: CauseNatural, exit: 7},
		{name: "prefix", command: "head -c 1 >/dev/null; exit 6", input: large, mode: ExecutionForeground, cause: CauseNatural, exit: 6},
		{name: "timeout", command: "sleep 30", input: large, mode: ExecutionForeground, timeout: 50 * time.Millisecond, cause: CauseTimeout},
		{name: "cancel", command: "sleep 30", input: large, mode: ExecutionForeground, cancelOnStart: true, cause: CauseClientCanceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := m.Ensure(context.Background(), "stdin-"+tc.name)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timeout := tc.timeout
			if timeout == 0 {
				timeout = 3 * time.Second
			}
			execution, err := m.StartExecution(ctx, b, tc.mode, tc.command, "", tc.input, nil, timeout, func(ExecutionStatus) {
				if tc.cancelOnStart {
					cancel()
				}
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan ExecutionResult, 1)
			go func() { done <- execution.Wait() }()
			var result ExecutionResult
			select {
			case result = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("command completion blocked by stdin")
			}
			if result.ExecutorBackend != backend || result.Cause != tc.cause {
				t.Fatalf("execution result = %+v", result)
			}
			if tc.cause == CauseNatural && (result.Process.Kind != executor.ProcessExited || result.Process.ExitCode != tc.exit) {
				t.Fatalf("input delivery replaced child result: %+v", result)
			}
			if tc.file {
				data, err := os.ReadFile(filepath.Join(b.Workspace(), "result"))
				if err != nil || string(data) != tc.input {
					t.Fatalf("saved input: bytes=%d want=%d err=%v", len(data), len(tc.input), err)
				}
			}
		})
	}
	t.Run("start_failure", func(t *testing.T) {
		b, err := m.Ensure(context.Background(), "stdin-start-failure")
		if err != nil {
			t.Fatal(err)
		}
		m.shellPath = filepath.Join(t.TempDir(), "missing-shell")
		done := make(chan error, 1)
		go func() {
			_, err := m.StartExecution(context.Background(), b, ExecutionForeground, "cat", "", large, nil, time.Second, nil, nil)
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("missing executable unexpectedly started")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("startup failure blocked by stdin")
		}
	})
}

func TestCommandInputCloseUnblocksWriter(t *testing.T) {
	input, err := newCommandInput(strings.Repeat("x", 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { input.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("input writer did not stop without a consuming process")
	}
}
