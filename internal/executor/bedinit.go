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
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/qiankunli/go-stdx/randx"
	"github.com/qiankunli/hostel/internal/bedinit"
	"github.com/qiankunli/hostel/internal/resource"
)

type BedInitFactory struct {
	exe       string
	socketDir string
	resources resource.Tracker
}

func NewBedInitFactory(exe string, resources resource.Tracker) (*BedInitFactory, error) {
	dir, err := os.MkdirTemp("", "hostel-executor-*")
	if err != nil {
		return nil, err
	}
	if resources == nil {
		resources = resource.Noop("resource tracker not configured")
	}
	return &BedInitFactory{exe: exe, socketDir: dir, resources: resources}, nil
}

func (*BedInitFactory) Backend() string { return "bed_init" }

func (f *BedInitFactory) Close() error {
	return os.RemoveAll(f.socketDir)
}

func (f *BedInitFactory) Create(ctx context.Context, bedID string) (Executor, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	executorID := "executor-" + randx.Hex(8)
	socket := filepath.Join(f.socketDir, executorID+".sock")
	cmd := exec.Command(f.exe, bedinit.InitArg,
		"--socket", socket,
		"--bed", bedID,
		"--executor", executorID,
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	// SIGTERM lets bed-init run its graceful Shutdown path and publish child
	// terminal statuses when Hostel exits unexpectedly.
	setPdeathsig(cmd, syscall.SIGTERM)
	releaseGroup, err := bindProcessCgroup(cmd, f.resources, bedID)
	if err != nil {
		return nil, fmt.Errorf("executor: prepare cgroup for bed %s: %w", bedID, err)
	}
	defer releaseGroup()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("executor: start bed-init for bed %s: %w", bedID, err)
	}
	e := &bedInitExecutor{
		id:        executorID,
		bedID:     bedID,
		socket:    socket,
		cmd:       cmd,
		proc:      cmd.Process,
		client:    bedinit.NewClient(socket, executorID),
		resources: f.resources,
		state:     StateStarting,
		done:      make(chan struct{}),
	}
	go e.watch()

	for range 100 {
		if err := e.client.Describe(); err == nil {
			e.mu.Lock()
			if e.state == StateStarting {
				e.state = StateReady
			}
			e.mu.Unlock()
			return e, nil
		}
		select {
		case <-e.done:
			return nil, fmt.Errorf("executor: bed-init %s exited before serving", executorID)
		case <-ctx.Done():
			e.forceLoss(ctx.Err())
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	e.forceLoss(errors.New("executor readiness timeout"))
	return nil, fmt.Errorf("executor: bed-init %s never became ready", executorID)
}

// Probe verifies the complete create/start/wait/shutdown path before the
// factory is selected for request traffic.
func (f *BedInitFactory) Probe(ctx context.Context) error {
	const bedID = "executor-probe"
	executor, err := f.Create(ctx, bedID)
	if err != nil {
		return err
	}
	defer func() { _ = f.resources.Release(bedID) }()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer devnull.Close()
	sink, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer sink.Close()
	cmd := exec.Command("/bin/true")
	cmd.Env = os.Environ()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, sink, sink
	process, err := executor.Start(ctx, "process-probe", cmd)
	if err != nil {
		_ = executor.Shutdown(ctx)
		return err
	}
	outcome, err := process.Wait(ctx)
	if err != nil || outcome.Kind != ProcessExited || outcome.ExitCode != 0 {
		_ = executor.Shutdown(ctx)
		return fmt.Errorf("executor probe: outcome=%+v err=%v", outcome, err)
	}
	return executor.Shutdown(ctx)
}

type bedInitExecutor struct {
	id        string
	bedID     string
	socket    string
	cmd       *exec.Cmd
	proc      *os.Process
	client    *bedinit.Client
	resources resource.Tracker

	mu           sync.Mutex
	state        State
	exit         Exit
	shutdown     bool
	forcedLoss   error
	done         chan struct{}
	publishOnce  sync.Once
	shutdownOnce sync.Once
	cleanupOnce  sync.Once
}

func (e *bedInitExecutor) ID() string            { return e.id }
func (e *bedInitExecutor) BedID() string         { return e.bedID }
func (*bedInitExecutor) Backend() string         { return "bed_init" }
func (e *bedInitExecutor) Done() <-chan struct{} { return e.done }

func (e *bedInitExecutor) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

func (e *bedInitExecutor) Exit() Exit {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.exit
}

func (e *bedInitExecutor) Start(ctx context.Context, processID string, cmd *exec.Cmd) (Process, error) {
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
	state := e.state
	e.mu.Unlock()
	if state != StateReady {
		return nil, fmt.Errorf("executor %s is %s", e.id, state)
	}
	stdin, closeStdin, err := commandFile(cmd.Stdin, "stdin", os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer closeStdin()
	stdout, closeStdout, err := commandFile(cmd.Stdout, "stdout", os.O_WRONLY)
	if err != nil {
		return nil, err
	}
	defer closeStdout()
	stderr, closeStderr, err := commandFile(cmd.Stderr, "stderr", os.O_WRONLY)
	if err != nil {
		return nil, err
	}
	defer closeStderr()
	argv := append([]string{cmd.Path}, cmd.Args[1:]...)
	const maxAttempts = 2
	var pid int
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		pid, err = e.client.Start(processID, argv, cmd.Dir, cmd.Env, stdin, stdout, stderr)
		if err == nil {
			if attempt > 1 {
				e.recordTransportRecovered(ctx, "start", processID, attempt)
			}
			return &bedInitProcess{id: processID, pid: pid, executor: e}, nil
		}
		var remoteErr *bedinit.RemoteError
		if errors.As(err, &remoteErr) {
			return nil, err
		}
		select {
		case <-e.done:
			e.recordTransportFailure(ctx, "start", processID, attempt, maxAttempts, false, err)
			return nil, fmt.Errorf("executor %s lost while starting %s", e.id, processID)
		default:
		}
		willRetry := attempt < maxAttempts
		e.recordTransportFailure(ctx, "start", processID, attempt, maxAttempts, willRetry, err)
		if willRetry {
			time.Sleep(10 * time.Millisecond)
		}
	}
	e.forceLoss(err)
	return nil, fmt.Errorf("executor %s became unreachable while starting %s", e.id, processID)
}

func commandFile(value any, name string, flag int) (*os.File, func(), error) {
	if value == nil {
		file, err := os.OpenFile(os.DevNull, flag, 0)
		if err != nil {
			return nil, nil, err
		}
		return file, func() { _ = file.Close() }, nil
	}
	file, ok := value.(*os.File)
	if !ok {
		return nil, nil, fmt.Errorf("executor: %s must be *os.File, got %T", name, value)
	}
	return file, func() {}, nil
}

func (e *bedInitExecutor) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.shutdownOnce.Do(func() {
		e.mu.Lock()
		e.shutdown = true
		if e.state == StateReady || e.state == StateStarting {
			e.state = StateDraining
		}
		e.mu.Unlock()
		if err := e.client.Shutdown(); err != nil {
			_ = e.proc.Signal(syscall.SIGTERM)
		}
	})
	select {
	case <-e.done:
		if exit := e.Exit(); exit.State == StateLost {
			return exit.Err
		}
		return nil
	case <-ctx.Done():
		e.forceLoss(ctx.Err())
		_ = e.proc.Kill()
		return ctx.Err()
	}
}

func (e *bedInitExecutor) forceLoss(err error) {
	e.mu.Lock()
	if e.forcedLoss == nil {
		e.forcedLoss = err
	}
	if e.state != StateStopped {
		e.state = StateLost
	}
	e.mu.Unlock()
	_ = e.proc.Kill()
}

func (e *bedInitExecutor) watch() {
	waitErr := waitCommandBeforeReap(e.cmd, func(barrierErr error) error {
		if barrierErr != nil {
			return e.proc.Kill()
		}
		return nil
	})
	e.mu.Lock()
	state := StateLost
	exitErr := waitErr
	if e.forcedLoss != nil {
		exitErr = e.forcedLoss
	} else if e.shutdown {
		state = StateStopped
		exitErr = nil
	} else if exitErr == nil {
		exitErr = errors.New("executor exited unexpectedly")
	}
	e.state = state
	e.exit = Exit{State: state, Err: exitErr}
	e.mu.Unlock()
	e.cleanup()
	e.publishOnce.Do(func() { close(e.done) })
}

func (e *bedInitExecutor) cleanup() {
	e.cleanupOnce.Do(func() {
		_ = os.Remove(e.socket)
		_ = e.resources.Release(e.bedID)
	})
}

type bedInitProcess struct {
	id       string
	pid      int
	executor *bedInitExecutor
}

func (p *bedInitProcess) ID() string { return p.id }
func (p *bedInitProcess) PID() int   { return p.pid }

func (p *bedInitProcess) Kill() {
	if err := p.executor.client.Kill(p.id); err != nil {
		p.executor.forceLoss(err)
	}
}

func (p *bedInitProcess) Wait(ctx context.Context) (ProcessOutcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		status, err := p.executor.client.Wait(p.id)
		if err == nil {
			if attempt > 1 {
				p.executor.recordTransportRecovered(ctx, "wait", p.id, attempt)
			}
			switch status.Kind {
			case bedinit.ExitStatusExited:
				return Exited(status.ExitCode), nil
			case bedinit.ExitStatusSignaled:
				return Signaled(status.Signal, status.CoreDumped), nil
			default:
				return Lost(p.executor.id, fmt.Errorf("unknown exit kind %q", status.Kind)), nil
			}
		}
		lastErr = err
		select {
		case <-p.executor.done:
			p.executor.recordTransportFailure(ctx, "wait", p.id, attempt, maxAttempts, false, err)
			return Lost(p.executor.id, p.executor.Exit().Err), nil
		case <-ctx.Done():
			p.executor.recordTransportFailure(ctx, "wait", p.id, attempt, maxAttempts, false, err)
			return ProcessOutcome{}, ctx.Err()
		default:
		}
		willRetry := attempt < maxAttempts
		p.executor.recordTransportFailure(ctx, "wait", p.id, attempt, maxAttempts, willRetry, err)
		if willRetry {
			select {
			case <-p.executor.done:
				return Lost(p.executor.id, p.executor.Exit().Err), nil
			case <-ctx.Done():
				return ProcessOutcome{}, ctx.Err()
			case <-time.After(time.Duration(attempt) * 10 * time.Millisecond):
			}
		}
	}
	p.executor.forceLoss(lastErr)
	select {
	case <-p.executor.done:
	case <-ctx.Done():
		return ProcessOutcome{}, ctx.Err()
	}
	return Lost(p.executor.id, lastErr), nil
}
