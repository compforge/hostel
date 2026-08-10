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
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"

	"github.com/qiankunli/hostel/internal/resource"
)

// Spawner is the seam between "what to run" and "who forks it"
// (docs/kernel.md 〈进程树〉). The Manager builds the fully-specified command —
// argv, dir, env, isolation already applied, stdio as concrete *os.File pipe
// ends (never StdinPipe/StdoutPipe conveniences: raw fds must be extractable to
// hand to an out-of-process spawner) — and the Spawner owns process lifetime:
// starting, killing, and the per-bed sweep at teardown.
//
// Implementations: inProcSpawner (all platforms, commands are the daemon's
// children, liveness tracked in a map) and, on linux, the bed-init spawner
// (S1: commands are children of the bed's own init, teardown = kill its tree).
type Spawner interface {
	// Name identifies the active process-lifetime implementation in execution
	// records. It is a bounded diagnostic dimension, not a user-facing tier.
	Name() string
	// Start launches cmd on behalf of the bed and returns its handle.
	Start(bedID string, cmd *exec.Cmd) (Proc, error)
	// KillBed force-kills every live process started for the bed. The safety
	// net behind per-process kills: it must not rely on callers having waited.
	KillBed(bedID string)
}

// ProcessOutcome is the kernel-level terminal fact for one started process.
// It deliberately does not contain timeout/cancel/teardown intent: the caller
// that requested a stop owns that fact and combines it into ExecutionResult.
type ProcessOutcome struct {
	Kind       ProcessOutcomeKind
	ExitCode   int
	Signal     int
	CoreDumped bool
	Error      string
}

type ProcessOutcomeKind string

const (
	ProcessExited   ProcessOutcomeKind = "exited"
	ProcessSignaled ProcessOutcomeKind = "signaled"
	ProcessLost     ProcessOutcomeKind = "lost"
)

func exitedProcess(code int) ProcessOutcome {
	return ProcessOutcome{Kind: ProcessExited, ExitCode: code}
}

func signaledProcess(signal int, coreDumped bool) ProcessOutcome {
	return ProcessOutcome{Kind: ProcessSignaled, Signal: signal, CoreDumped: coreDumped}
}

func lostProcess(err error) ProcessOutcome {
	return ProcessOutcome{Kind: ProcessLost, Error: err.Error()}
}

// Proc is one started bed process.
type Proc interface {
	Pid() int
	// Kill force-kills the process group.
	Kill()
	// Wait blocks until the process terminates and preserves whether the kernel
	// observed a normal exit, a signal, or a lost wait channel.
	// Call at most once.
	Wait() ProcessOutcome
}

// inProcSpawner forks commands as direct children of the daemon and tracks
// live process groups per bed so teardown can sweep them. Every command is
// started with Setpgid, so pid == pgid and Kill(-pid) takes the whole tree
// (modulo setsid escapees — the bed-init spawner closes that gap on linux).
type inProcSpawner struct {
	resources resource.Tracker
	mu        sync.Mutex
	live      map[string]map[int]*inProcProc // bedID → live process groups
}

func newInProcSpawner(resources resource.Tracker) *inProcSpawner {
	return &inProcSpawner{resources: resources, live: make(map[string]map[int]*inProcProc)}
}

func (*inProcSpawner) Name() string { return "in_process" }

func (s *inProcSpawner) Start(bedID string, cmd *exec.Cmd) (Proc, error) {
	if cmd.Env == nil {
		return nil, errors.New("bed: process environment must be explicit")
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true // one pgid per command: kill takes the tree
	releaseGroup, err := bindProcessCgroup(cmd, s.resources, bedID)
	if err != nil {
		return nil, err
	}
	defer releaseGroup()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pid := cmd.Process.Pid
	proc := &inProcProc{
		cmd:     cmd,
		pid:     pid,
		untrack: func() { s.untrack(bedID, pid) },
	}
	s.mu.Lock()
	if s.live[bedID] == nil {
		s.live[bedID] = make(map[int]*inProcProc)
	}
	s.live[bedID][pid] = proc
	s.mu.Unlock()
	return proc, nil
}

func (s *inProcSpawner) untrack(bedID string, pid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pids, ok := s.live[bedID]; ok {
		delete(pids, pid)
		if len(pids) == 0 {
			delete(s.live, bedID)
		}
	}
}

func (s *inProcSpawner) KillBed(bedID string) {
	s.mu.Lock()
	procs := make([]*inProcProc, 0, len(s.live[bedID]))
	for _, proc := range s.live[bedID] {
		procs = append(procs, proc)
	}
	s.mu.Unlock()
	for _, proc := range procs {
		proc.Kill()
	}
}

type inProcProc struct {
	cmd      *exec.Cmd
	pid      int
	untrack  func()
	once     sync.Once
	signalMu sync.Mutex
	exited   bool
}

func (p *inProcProc) Pid() int { return p.pid }

func (p *inProcProc) Kill() {
	p.signalMu.Lock()
	defer p.signalMu.Unlock()
	if p.exited {
		return
	}
	_ = signalProcessGroup(p.pid, syscall.SIGKILL)
}

func (p *inProcProc) Wait() ProcessOutcome {
	err := waitCommandBeforeReap(p.cmd, p.markExitedBeforeReap)
	if err == nil {
		return exitedProcess(0)
	}
	if ee, ok := err.(*exec.ExitError); ok {
		if status, ok := ee.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return signaledProcess(int(status.Signal()), status.CoreDump())
		}
		return exitedProcess(ee.ExitCode())
	}
	return lostProcess(err)
}

// markExitedBeforeReap publishes the terminal state while Linux still
// reserves the numeric PID/PGID. Kill and KillBed use the same mutex, so a
// signal either happens before reap or is skipped after this point.
func (p *inProcProc) markExitedBeforeReap(barrierErr error) error {
	p.signalMu.Lock()
	defer p.signalMu.Unlock()
	if barrierErr != nil {
		// Without the WNOWAIT barrier the child may still be running. Force it
		// terminal before publishing exited; cmd.Wait will reap it immediately
		// after this callback returns, so its numeric PID remains reserved
		// throughout the transition.
		if err := signalProcessGroup(p.pid, syscall.SIGKILL); err != nil &&
			!errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("bed: kill pid %d after exit barrier failure: %w", p.pid, err)
		}
	}
	p.exited = true
	p.once.Do(p.untrack)
	return nil
}

var signalProcessGroup = func(pid int, signal syscall.Signal) error {
	return syscall.Kill(-pid, signal)
}
