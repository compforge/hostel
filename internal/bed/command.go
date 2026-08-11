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
	"os/exec"
	"time"

	"github.com/qiankunli/go-stdx/randx"
	"github.com/qiankunli/go-stdx/shellx"
	"github.com/qiankunli/hostel/internal/executor"
)

// buildCommand constructs an isolated `bash -c <command>` for the bed. envs are
// an invocation-scoped overlay; cwd (host path) overrides the workspace.
func (m *Manager) buildCommand(b *Bed, command, cwdInBed string, envs map[string]string) (*exec.Cmd, error) {
	m.touchBed(b)
	// Apply cwd with a `cd` INSIDE the command (same mechanism the session shell
	// uses), NOT via cmd.Dir. Under suite cwdInBed is a sandbox-internal path
	// (/workspace/…) that doesn't exist on the carrier host, so setting it as
	// the outer (bwrap) process's Dir makes ForkExec's chdir fail with ENOENT
	// ("bedinit: spawn: fork: no such file or directory"). The cd runs in the
	// command's own view — inside bwrap under suite, directly under direct —
	// where cwdInBed is valid (web.resolveCwd materialized the dir via EnsureDir).
	if cwdInBed != "" {
		command = "cd -- " + shellx.Quote(cwdInBed) + " && { " + command + " ; }"
	}
	cmd := exec.Command(m.shellPath, shellCommandArgs(m.shellPath, command)...)
	if err := m.iso.Wrap(cmd, b.BedFS()); err != nil {
		return nil, err
	}
	// The OUTER process cwd must exist on the host; the bed's own workspace
	// always does (the in-sandbox cwd is handled by the cd above / bwrap --chdir).
	cmd.Dir = b.Workspace()
	env, err := m.buildBedEnv(b, envs)
	if err != nil {
		return nil, err
	}
	cmd.Env = env
	return cmd, nil
}

// startOneShot builds and launches an isolated one-shot command in the Bed's
// current Executor. Explicit pipes preserve output across the bed-init IPC seam.
func (m *Manager) startOneShot(ctx context.Context, b *Bed, command, cwdInBed string, envs map[string]string) (executor.Process, executor.Executor, *os.File, *os.File, error) {
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
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	bedExecutor, err := b.executorFor(ctx, m.executorFactory)
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
func (m *Manager) StartExecution(
	ctx context.Context,
	b *Bed,
	mode ExecutionMode,
	command, cwdInBed string,
	envs map[string]string,
	timeout time.Duration,
	onStart func(ExecutionStatus),
	onOutput func(ExecutionOutput),
) (*Execution, error) {
	finishOperation, err := m.BeginOperation(b, OpExec, timeout)
	if err != nil {
		return nil, err
	}
	proc, bedExecutor, stdout, stderr, err := m.startOneShot(ctx, b, command, cwdInBed, envs)
	if err != nil {
		finishOperation()
		return nil, err
	}
	execution := m.executions.track(ctx, b.ID, mode, bedExecutor.ID(), bedExecutor.Backend(), proc, stdout, stderr, timeout, onStart, onOutput, func(result ExecutionResult) {
		finishOperation()
		b.RecordCommand(result.Duration)
	})
	return execution, nil
}

// RunForeground executes a one-shot command as a fresh, isolated `bash -c`
// process, streams typed output, and waits for its structured terminal result.
func (m *Manager) RunForeground(ctx context.Context, b *Bed, command, cwdInBed string, envs map[string]string, timeout time.Duration, onOutput func(ExecutionOutput)) (ExecutionResult, error) {
	execution, err := m.StartExecution(ctx, b, ExecutionForeground, command, cwdInBed, envs, timeout, nil, onOutput)
	if err != nil {
		return ExecutionResult{}, err
	}
	return execution.Wait(), nil
}
