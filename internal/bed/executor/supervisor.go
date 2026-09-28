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
	"github.com/qiankunli/hostel/internal/bed/tool"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/qiankunli/go-stdx/randx"
	"github.com/qiankunli/hostel/internal/bed/executor/supervisor"
	"github.com/qiankunli/hostel/internal/bed/resource"
)

type SupervisorFactory struct {
	pidns               tool.Status
	privatePIDNamespace bool
	exe                 string
	socketDir           string
	resources           resource.Tracker
}

func NewSupervisorFactory(exe string, resources resource.Tracker) (*SupervisorFactory, error) {
	dir, err := os.MkdirTemp("", "hostel-executor-*")
	if err != nil {
		return nil, err
	}
	if resources == nil {
		resources = resource.Noop("resource tracker not configured")
	}
	return &SupervisorFactory{exe: exe, socketDir: dir, resources: resources, pidns: pidnsStatus(tool.Off, false, false, "")}, nil
}

func (*SupervisorFactory) Backend() string { return "supervisor" }
func (f *SupervisorFactory) Status() Status {
	return Status{Backend: f.Backend(), Tools: map[string]tool.Status{"pidns": f.pidns}}
}

func (f *SupervisorFactory) Close() error {
	return os.RemoveAll(f.socketDir)
}

func (f *SupervisorFactory) Create(ctx context.Context, bedID string) (_ Executor, retErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	executorID := "executor-" + randx.Hex(8)
	group, err := f.resources.ExecutorGroup(bedID, executorID)
	if err != nil {
		return nil, err
	}
	started := false
	defer func() {
		if !started {
			_ = group.Close()
		}
	}()
	socket := filepath.Join(f.socketDir, executorID+".sock")
	cmd := exec.Command(f.exe, supervisor.Arg,
		"--socket", socket,
		"--bed", bedID,
		"--executor", executorID,
	)
	if f.privatePIDNamespace {
		releaseNamespace, err := preparePIDNamespace(cmd)
		if err != nil {
			return nil, err
		}
		defer releaseNamespace()
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	// SIGTERM lets the supervisor run its graceful Shutdown path and publish child
	// terminal statuses when Hostel exits unexpectedly.
	setPdeathsig(cmd, syscall.SIGTERM)
	releaseGroup, err := bindProcessCgroup(cmd, group)
	if err != nil {
		return nil, fmt.Errorf("executor: prepare cgroup for bed %s: %w", bedID, err)
	}
	defer releaseGroup()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("executor: start supervisor for bed %s: %w", bedID, err)
	}
	started = true
	e := &supervisedExecutor{
		id:     executorID,
		bedID:  bedID,
		socket: socket,
		cmd:    cmd,
		proc:   cmd.Process,
		client: supervisor.NewClient(socket, executorID),
		group:  group,
		state:  StateStarting,
		done:   make(chan struct{}),
	}
	go e.watch()
	defer func() {
		if retErr == nil {
			return
		}
		e.forceLoss(retErr)
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.Shutdown(cleanup); err != nil {
			retErr = errors.Join(retErr, &ProbeCleanupError{Err: err})
		}
	}()

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
			return nil, fmt.Errorf("executor: supervisor %s exited before serving", executorID)
		case <-ctx.Done():
			e.forceLoss(ctx.Err())
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	e.forceLoss(errors.New("executor readiness timeout"))
	return nil, fmt.Errorf("executor: supervisor %s never became ready", executorID)
}

// ProbeCleanupError prevents startup fallback while a candidate still owns resources.
type ProbeCleanupError struct{ Err error }

func (e *ProbeCleanupError) Error() string { return "executor probe cleanup: " + e.Err.Error() }
func (e *ProbeCleanupError) Unwrap() error { return e.Err }

// Probe verifies the complete create/start/wait/shutdown path before selection.
func (f *SupervisorFactory) Probe(ctx context.Context) (retErr error) {
	const bedID = "executor-probe"
	executor, err := f.Create(ctx, bedID)
	if err != nil {
		var cleanup *ProbeCleanupError
		if !errors.As(err, &cleanup) {
			if releaseErr := f.resources.Release(bedID); releaseErr != nil {
				err = errors.Join(err, &ProbeCleanupError{Err: releaseErr})
			}
		}
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := executor.Shutdown(cleanup); err != nil {
			retErr = errors.Join(retErr, &ProbeCleanupError{Err: err})
			return
		}
		if err := f.resources.Release(bedID); err != nil {
			retErr = errors.Join(retErr, &ProbeCleanupError{Err: err})
		}
	}()
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
		return err
	}
	outcome, err := process.Wait(ctx)
	if err != nil || outcome.Kind != ProcessExited || outcome.ExitCode != 0 {
		return fmt.Errorf("executor probe: outcome=%+v err=%v", outcome, err)
	}
	return nil
}

type supervisedExecutor struct {
	id     string
	bedID  string
	socket string
	cmd    *exec.Cmd
	proc   *os.Process
	client *supervisor.Client
	group  resource.Group

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

func (e *supervisedExecutor) ID() string            { return e.id }
func (e *supervisedExecutor) BedID() string         { return e.bedID }
func (*supervisedExecutor) Backend() string         { return "supervisor" }
func (e *supervisedExecutor) Done() <-chan struct{} { return e.done }

func (e *supervisedExecutor) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

func (e *supervisedExecutor) Exit() Exit {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.exit
}

func (e *supervisedExecutor) Start(ctx context.Context, processID string, cmd *exec.Cmd) (Process, error) {
	return e.start(ctx, processID, cmd, false)
}

func (e *supervisedExecutor) StartService(ctx context.Context, processID string, cmd *exec.Cmd) (Process, error) {
	return e.start(ctx, processID, cmd, true)
}

func (e *supervisedExecutor) start(ctx context.Context, processID string, cmd *exec.Cmd, drainGroup bool) (Process, error) {
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
		if drainGroup {
			pid, err = e.client.StartService(processID, argv, cmd.Dir, cmd.Env, stdin, stdout, stderr, cmd.ExtraFiles...)
		} else {
			pid, err = e.client.Start(processID, argv, cmd.Dir, cmd.Env, stdin, stdout, stderr, cmd.ExtraFiles...)
		}
		if err == nil {
			if attempt > 1 {
				e.recordTransportRecovered(ctx, "start", processID, attempt)
			}
			return &supervisedProcess{id: processID, pid: pid, executor: e}, nil
		}
		var remoteErr *supervisor.RemoteError
		if errors.As(err, &remoteErr) {
			return nil, err
		}
		var requestErr *supervisor.RequestError
		if errors.As(err, &requestErr) {
			// A locally rejected Start says nothing about supervisor health.
			// Retrying the same specification or replacing the shared Executor
			// would only disrupt its unrelated commands and services.
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

func (e *supervisedExecutor) Shutdown(ctx context.Context) error {
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
		return e.group.Close()
	case <-ctx.Done():
		e.forceLoss(ctx.Err())
		_ = e.proc.Kill()
		return ctx.Err()
	}
}

func (e *supervisedExecutor) forceLoss(err error) {
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

func (e *supervisedExecutor) watch() {
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

func (e *supervisedExecutor) cleanup() {
	e.cleanupOnce.Do(func() {
		_ = os.Remove(e.socket)
		if err := e.group.Close(); err != nil {
			e.mu.Lock()
			e.state = StateLost
			e.exit = Exit{State: StateLost, Err: err}
			e.mu.Unlock()
		}
	})
}

type supervisedProcess struct {
	id       string
	pid      int
	executor *supervisedExecutor
}

func (p *supervisedProcess) ID() string { return p.id }
func (p *supervisedProcess) PID() int   { return p.pid }

func (p *supervisedProcess) Signal(signal syscall.Signal) error {
	return p.executor.client.Signal(p.id, signal)
}

func (p *supervisedProcess) Kill() {
	if err := p.executor.client.Kill(p.id); err != nil {
		p.executor.forceLoss(err)
	}
}

func (p *supervisedProcess) Wait(ctx context.Context) (ProcessOutcome, error) {
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
			case supervisor.ExitStatusExited:
				return Exited(status.ExitCode), nil
			case supervisor.ExitStatusSignaled:
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
