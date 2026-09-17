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
	"os/exec"
	"time"

	"github.com/qiankunli/go-stdx/randx"
	"github.com/qiankunli/hostel/internal/bed/executor"
)

// buildCommand constructs an isolated `bash -c <command>` for the bed. envs are
// an invocation-scoped overlay; cwd is a carrier BedFS path and an empty value
// selects the workspace.
//
// +rule=`Treat command as opaque bash source; apply cwd through the process view and never rewrite caller text.`
func (m *Manager) buildCommand(b *managedBed, command, cwd string, envs map[string]string) (*exec.Cmd, error) {
	// The interpreter is daemon configuration, unlike Service workload argv.
	if _, err := exec.LookPath(m.shellPath); err != nil {
		return nil, err
	}
	m.touchBed(b)
	cmd := exec.Command(m.shellPath, shellCommandArgs(m.shellPath, command)...)
	env, err := m.buildExecutionEnv(b, envs)
	if err != nil {
		return nil, err
	}
	cmd.Env = env
	if err := b.environment.Wrap(cmd, cwd); err != nil {
		return nil, err
	}
	return cmd, nil
}

// startOneShot builds and launches an isolated one-shot command in the Bed's
// current Executor. Explicit pipes preserve output across the supervisor IPC seam.
func (m *Manager) startOneShot(ctx context.Context, b *managedBed, command, cwdInBed string, envs map[string]string, stdin *os.File) (executor.Process, executor.Executor, *os.File, *os.File, error) {
	cmd, err := m.buildCommand(b, command, cwdInBed, envs)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdoutR.Close()
		stdoutW.Close()
		return nil, nil, nil, nil, err
	}
	if stdin != nil {
		cmd.Stdin = stdin
	}
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	bedExecutor, err := m.executorManager.For(ctx, b.Bed)
	if err == nil {
		procID := "process-" + randx.Hex(8)
		var proc executor.Process
		proc, err = bedExecutor.Start(ctx, procID, cmd)
		stdoutW.Close()
		stderrW.Close()
		if err == nil {
			return proc, bedExecutor, stdoutR, stderrR, nil
		}
	} else {
		stdoutW.Close()
		stderrW.Close()
	}
	stdoutR.Close()
	stderrR.Close()
	return nil, nil, nil, nil, err
}

// StartExecution launches and registers one command. Foreground and background
// share the same lifecycle; callers only choose whether to wait for Result.
// Stdin is finite input followed by EOF; empty input supplies immediate EOF.
func (m *Manager) StartExecution(
	ctx context.Context,
	b *managedBed,
	mode ExecutionMode,
	command, cwdInBed, stdin string,
	envs map[string]string,
	timeout time.Duration,
	onStart func(ExecutionStatus),
	onOutput func(ExecutionOutput),
) (*Execution, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	finishOperation, err := m.BeginOperation(b, OpExec, timeout)
	if err != nil {
		return nil, err
	}
	var outputLog *executionLog
	if mode == ExecutionBackground {
		var err error
		outputLog, err = newExecutionLog(b.Spec().Dir)
		if err != nil {
			finishOperation()
			return nil, err
		}
	}
	cleanupLog := func() {
		if outputLog != nil {
			outputLog.discard()
		}
	}
	input, err := newCommandInput(stdin)
	if err != nil {
		cleanupLog()
		finishOperation()
		return nil, err
	}
	proc, bedExecutor, stdout, stderr, err := m.startOneShot(ctx, b, command, cwdInBed, envs, input.file())
	// The child owns its inherited descriptor after Start; retaining our read
	// end would prevent a writer from observing an early child-side close.
	input.closeReader()
	if err != nil {
		input.close()
		cleanupLog()
		finishOperation()
		return nil, err
	}
	execution := m.executions.track(ctx, b.Name, mode, bedExecutor.ID(), bedExecutor.Backend(), proc, stdout, stderr, timeout, command, outputLog, onStart, onOutput, func(result ExecutionResult) {
		input.close()
		finishOperation()
		b.RecordCommand(result.Duration)
	})
	return execution, nil
}

// RunForeground executes a one-shot command as a fresh, isolated `bash -c`
// process, streams typed output, and waits for its structured terminal result.
func (m *Manager) RunForeground(ctx context.Context, b *managedBed, command, cwdInBed string, envs map[string]string, timeout time.Duration, onOutput func(ExecutionOutput)) (ExecutionResult, error) {
	execution, err := m.StartExecution(ctx, b, ExecutionForeground, command, cwdInBed, "", envs, timeout, nil, onOutput)
	if err != nil {
		return ExecutionResult{}, err
	}
	return execution.Wait(), nil
}
