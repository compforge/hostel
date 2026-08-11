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
	"os/exec"
	"sync"
	"syscall"

	"github.com/qiankunli/go-stdx/randx"
	"github.com/qiankunli/hostel/internal/resource"
)

// LocalFactory creates Executors whose processes are direct Hostel children.
// It is the portable backend and the honest fallback when supervisor is absent.
type LocalFactory struct {
	resources resource.Tracker
}

func NewLocalFactory(resources resource.Tracker) *LocalFactory {
	if resources == nil {
		resources = resource.Noop("resource tracker not configured")
	}
	return &LocalFactory{resources: resources}
}

func (*LocalFactory) Backend() string { return "local" }
func (*LocalFactory) Close() error    { return nil }

func (f *LocalFactory) Create(_ context.Context, bedID string) (Executor, error) {
	return &localExecutor{
		id:        "executor-" + randx.Hex(8),
		bedID:     bedID,
		state:     StateReady,
		resources: f.resources,
		processes: make(map[string]*localProcess),
		done:      make(chan struct{}),
	}, nil
}

type localExecutor struct {
	id        string
	bedID     string
	resources resource.Tracker

	mu        sync.Mutex
	state     State
	processes map[string]*localProcess
	exit      Exit
	done      chan struct{}
	stopOnce  sync.Once
}

func (e *localExecutor) ID() string            { return e.id }
func (e *localExecutor) BedID() string         { return e.bedID }
func (*localExecutor) Backend() string         { return "local" }
func (e *localExecutor) Done() <-chan struct{} { return e.done }

func (e *localExecutor) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

func (e *localExecutor) Exit() Exit {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.exit
}

func (e *localExecutor) Start(ctx context.Context, processID string, cmd *exec.Cmd) (Process, error) {
	if processID == "" {
		return nil, errors.New("executor: process id is required")
	}
	if cmd == nil || len(cmd.Args) == 0 {
		return nil, errors.New("executor: command is required")
	}
	if cmd.Env == nil {
		return nil, errors.New("executor: process environment must be explicit")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state != StateReady {
		return nil, fmt.Errorf("executor %s is %s", e.id, e.state)
	}
	if existing, ok := e.processes[processID]; ok {
		if existing.specHash != processSpecHash(cmd) {
			return nil, fmt.Errorf("executor: process id %s reused with different specification", processID)
		}
		return existing, nil
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	// Local has no supervisor process to outlive Hostel. Make the kernel kill
	// each direct child if its parent daemon disappears; process-group cleanup
	// remains the orderly-shutdown path.
	setPdeathsig(cmd, syscall.SIGKILL)
	releaseGroup, err := bindProcessCgroup(cmd, e.resources, e.bedID)
	if err != nil {
		return nil, err
	}
	defer releaseGroup()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	process := &localProcess{
		id:         processID,
		executorID: e.id,
		specHash:   processSpecHash(cmd),
		cmd:        cmd,
		pid:        cmd.Process.Pid,
		done:       make(chan struct{}),
	}
	e.processes[processID] = process
	go process.reap()
	return process, nil
}

func (e *localExecutor) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.stopOnce.Do(func() {
		e.mu.Lock()
		e.state = StateDraining
		processes := make([]*localProcess, 0, len(e.processes))
		for _, process := range e.processes {
			processes = append(processes, process)
		}
		e.mu.Unlock()

		for _, process := range processes {
			process.Kill()
		}
		for _, process := range processes {
			select {
			case <-process.done:
			case <-ctx.Done():
				_ = e.resources.Release(e.bedID)
				e.finish(StateLost, ctx.Err())
				return
			}
		}
		_ = e.resources.Release(e.bedID)
		e.finish(StateStopped, nil)
	})
	select {
	case <-e.done:
		if exit := e.Exit(); exit.State == StateLost {
			return exit.Err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *localExecutor) finish(state State, err error) {
	e.mu.Lock()
	if e.state == StateStopped || e.state == StateLost {
		e.mu.Unlock()
		return
	}
	e.state = state
	e.exit = Exit{State: state, Err: err}
	e.mu.Unlock()
	close(e.done)
}

type localProcess struct {
	id         string
	executorID string
	specHash   string
	cmd        *exec.Cmd
	pid        int

	signalMu sync.Mutex
	exited   bool
	mu       sync.Mutex
	outcome  ProcessOutcome
	done     chan struct{}
}

func (p *localProcess) ID() string { return p.id }
func (p *localProcess) PID() int   { return p.pid }

func (p *localProcess) Kill() {
	p.signalMu.Lock()
	defer p.signalMu.Unlock()
	if p.exited {
		return
	}
	_ = signalProcessGroup(p.pid, syscall.SIGKILL)
}

func (p *localProcess) Wait(ctx context.Context) (ProcessOutcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.outcome, nil
	case <-ctx.Done():
		return ProcessOutcome{}, ctx.Err()
	}
}

func (p *localProcess) reap() {
	err := waitCommandBeforeReap(p.cmd, p.markExitedBeforeReap)
	outcome := Exited(0)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				outcome = Signaled(int(status.Signal()), status.CoreDump())
			} else {
				outcome = Exited(exitErr.ExitCode())
			}
		} else {
			outcome = Lost(p.executorID, err)
		}
	}
	p.mu.Lock()
	p.outcome = outcome
	p.mu.Unlock()
	close(p.done)
}

func (p *localProcess) markExitedBeforeReap(barrierErr error) error {
	p.signalMu.Lock()
	defer p.signalMu.Unlock()
	if barrierErr != nil {
		if err := signalProcessGroup(p.pid, syscall.SIGKILL); err != nil &&
			!errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("executor: kill pid %d after exit barrier failure: %w", p.pid, err)
		}
	}
	p.exited = true
	return nil
}

var signalProcessGroup = func(pid int, signal syscall.Signal) error {
	return syscall.Kill(-pid, signal)
}
