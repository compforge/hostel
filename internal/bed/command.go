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
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/qiankunli/go-stdx/randx"
	"github.com/qiankunli/go-stdx/shellx"
	"github.com/qiankunli/hostel/internal/isolation"
)

// Command is one one-shot execution (spec /command), foreground or background.
// Output lines are buffered for the status/logs endpoints; a foreground caller
// additionally streams them live via the onLine callback.
type Command struct {
	ID    string
	BedID string

	mu         sync.Mutex
	lines      []string
	running    bool
	exitCode   *int
	err        string
	startedAt  time.Time
	finishedAt *time.Time

	proc Proc
	done chan struct{} // closed when the process has been reaped
}

// commandBufferLimit caps buffered output lines per command so a chatty
// background process can't grow the daemon unbounded; older lines are dropped
// (the cursor semantics of /logs still hold — indices keep increasing).
const commandBufferLimit = 100_000

// dropped counts lines evicted from the front; line i lives at lines[i-dropped].
type cursorState struct{ dropped int64 }

// CommandStatus is the command-state view for the status endpoint.
type CommandStatus struct {
	ID         string
	Running    bool
	ExitCode   *int
	Err        string
	StartedAt  time.Time
	FinishedAt *time.Time
	Content    string
}

// CommandRegistry tracks one-shot commands. Ids are daemon-global because the
// spec's status/logs endpoints don't carry a bed dimension.
type CommandRegistry struct {
	mu      sync.Mutex
	cmds    map[string]*Command
	cursors map[string]*cursorState
}

func newCommandRegistry() *CommandRegistry {
	return &CommandRegistry{cmds: make(map[string]*Command), cursors: make(map[string]*cursorState)}
}

// track registers an already-started process and drives it to completion:
// streams combined stdout/stderr from output into the buffer (and onLine when
// given), then records the exit code from proc.Wait. timeout > 0 kills the
// process group at the deadline. Process START belongs to the Spawner seam —
// the registry only bookkeeps, so it works identically whether the process is
// a daemon child or lives under the bed's init.
func (r *CommandRegistry) track(bedID string, proc Proc, output io.ReadCloser, timeout time.Duration, onLine func(string)) *Command {
	c := &Command{
		ID:        "cmd-" + randx.Hex(8),
		BedID:     bedID,
		running:   true,
		startedAt: time.Now(),
		proc:      proc,
		done:      make(chan struct{}),
	}

	r.mu.Lock()
	r.cmds[c.ID] = c
	r.cursors[c.ID] = &cursorState{}
	r.mu.Unlock()

	var timer *time.Timer
	if timeout > 0 {
		timer = time.AfterFunc(timeout, func() { c.Interrupt() })
	}

	go func() {
		reader := bufio.NewReader(output)
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				c.appendLine(r, line)
				if onLine != nil {
					onLine(line)
				}
			}
			if err != nil {
				break
			}
		}
		output.Close()
		code, werr := proc.Wait()
		if timer != nil {
			timer.Stop()
		}
		now := time.Now()
		c.mu.Lock()
		c.running = false
		c.finishedAt = &now
		if werr != nil {
			c.err = werr.Error()
		}
		c.exitCode = &code
		c.mu.Unlock()
		close(c.done)
	}()
	return c
}

func (c *Command) appendLine(r *CommandRegistry, line string) {
	c.mu.Lock()
	c.lines = append(c.lines, line)
	if len(c.lines) > commandBufferLimit {
		evict := len(c.lines) - commandBufferLimit
		c.lines = c.lines[evict:]
		r.mu.Lock()
		if cs, ok := r.cursors[c.ID]; ok {
			cs.dropped += int64(evict)
		}
		r.mu.Unlock()
	}
	c.mu.Unlock()
}

// Wait blocks until the process is reaped (foreground streaming).
func (c *Command) Wait() { <-c.done }

// Interrupt kills the process group.
func (c *Command) Interrupt() {
	c.mu.Lock()
	proc := c.proc
	running := c.running
	c.mu.Unlock()
	if running && proc != nil {
		proc.Kill()
	}
}

// Status snapshots the command for /command/status/{id}.
func (c *Command) Status() CommandStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return CommandStatus{
		ID:         c.ID,
		Running:    c.running,
		ExitCode:   c.exitCode,
		Err:        c.err,
		StartedAt:  c.startedAt,
		FinishedAt: c.finishedAt,
		Content:    strings.Join(c.lines, ""),
	}
}

// Get looks a command up by id.
func (r *CommandRegistry) Get(id string) (*Command, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.cmds[id]
	return c, ok
}

// Logs returns output lines after the 0-based line cursor (-1 = from start),
// plus the next cursor (last line index seen) and whether it is still running.
func (r *CommandRegistry) Logs(id string, cursor int64) (content string, next int64, running bool, err error) {
	r.mu.Lock()
	c, ok := r.cmds[id]
	cs := r.cursors[id]
	r.mu.Unlock()
	if !ok {
		return "", 0, false, fmt.Errorf("command %s not found", id)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	dropped := int64(0)
	if cs != nil {
		dropped = cs.dropped
	}
	total := dropped + int64(len(c.lines))
	start := cursor + 1 // lines after `cursor`
	if cursor < 0 {
		start = 0
	}
	if start < dropped {
		start = dropped // evicted lines are gone; resume at oldest retained
	}
	if start < total {
		content = strings.Join(c.lines[start-dropped:], "")
	}
	next = total - 1
	if next < 0 {
		next = 0
	}
	return content, next, c.running, nil
}

// killBed interrupts every command belonging to a bed (bed teardown).
func (r *CommandRegistry) killBed(bedID string) {
	r.mu.Lock()
	var victims []*Command
	for _, c := range r.cmds {
		if c.BedID == bedID {
			victims = append(victims, c)
		}
	}
	r.mu.Unlock()
	for _, c := range victims {
		c.Interrupt()
	}
}

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
	if err := m.iso.Wrap(cmd, isolation.Workspace{Home: b.Home, Path: b.Workspace}); err != nil {
		return nil, err
	}
	// The OUTER process cwd must exist on the host; the bed's own workspace
	// always does (the in-sandbox cwd is handled by the cd above / bwrap --chdir).
	cmd.Dir = b.Workspace
	env, err := m.buildBedEnv(b, envs)
	if err != nil {
		return nil, err
	}
	cmd.Env = env
	return cmd, nil
}

// startOneShot builds and launches an isolated one-shot command via the
// spawner, returning the proc and the read end of its combined stdout+stderr.
// The pipe is explicit (not StdoutPipe) so the raw child-side fd can cross a
// process boundary when the spawner is the bed's init.
func (m *Manager) startOneShot(b *Bed, command, cwdInBed string, envs map[string]string) (Proc, *os.File, error) {
	cmd, err := m.buildCommand(b, command, cwdInBed, envs)
	if err != nil {
		return nil, nil, err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw // interleave, like the /command spec
	proc, err := m.spawner.Start(b.ID, cmd)
	// Child holds its own copy now (or never will, on error): drop ours, or
	// the reader never sees EOF.
	pw.Close()
	if err != nil {
		pr.Close()
		return nil, nil, err
	}
	return proc, pr, nil
}

// StartCommand launches a one-shot command in the bed and registers it.
func (m *Manager) StartCommand(b *Bed, command, cwdInBed string, envs map[string]string, timeout time.Duration, onLine func(string)) (*Command, error) {
	finishOperation, err := m.BeginOperation(b, OpExec, timeout)
	if err != nil {
		return nil, err
	}
	proc, out, err := m.startOneShot(b, command, cwdInBed, envs)
	if err != nil {
		finishOperation()
		return nil, err
	}
	c := m.commands.track(b.ID, proc, out, timeout, onLine)
	go func() { // tally the run once it is reaped (background = async)
		c.Wait()
		finishOperation()
		st := c.Status()
		if st.FinishedAt != nil {
			b.RecordCommand(st.FinishedAt.Sub(st.StartedAt))
		}
	}()
	return c, nil
}

// RunForeground executes a one-shot command as a fresh, isolated `bash -c`
// process (execd parity: /command is stateless), streams combined stdout+stderr
// via onLine, and blocks until the process exits or ctx is cancelled. Returns
// the process exit code (-1 on a non-exit failure).
//
// Unlike StartCommand it is NOT registered in the daemon-global command registry
// (which never GCs — a per-exec entry there would leak) and reuses nothing: the
// command runs in its OWN process, so a caller script's `set -e` / `exit` /
// `trap` dies with it. The foreground /command path used to run in the bed's
// shared stateful shell, where exactly those constructs tore the whole session
// down ("shell: session exited during run"); the persistent shell now serves
// only the explicit /session endpoint.
func (m *Manager) RunForeground(ctx context.Context, b *Bed, command, cwdInBed string, envs map[string]string, timeout time.Duration, onLine func(string)) (int, error) {
	finishOperation, err := m.BeginOperation(b, OpExec, timeout)
	if err != nil {
		return -1, err
	}
	defer finishOperation()
	proc, out, err := m.startOneShot(b, command, cwdInBed, envs)
	if err != nil {
		return -1, err
	}
	start := time.Now()
	if timeout > 0 {
		t := time.AfterFunc(timeout, proc.Kill)
		defer t.Stop()
	}
	// Client disconnect / shutdown cancels the run: kill the tree so a runaway
	// command can't outlive its caller.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			proc.Kill()
		case <-stop:
		}
	}()
	reader := bufio.NewReader(out)
	for {
		line, rerr := reader.ReadString('\n')
		if line != "" && onLine != nil {
			onLine(line)
		}
		if rerr != nil {
			break
		}
	}
	out.Close()
	code, werr := proc.Wait()
	b.RecordCommand(time.Since(start))
	if werr != nil {
		return -1, werr
	}
	return code, nil
}

// OutputStream identifies one side of a typed foreground command stream.
type OutputStream string

const (
	StreamStdout OutputStream = "stdout"
	StreamStderr OutputStream = "stderr"
)

// OutputLine is one line from a foreground command's stdout or stderr. Unlike
// RunForeground, this API preserves the two streams end-to-end for HTTP
// clients that expose exec semantics rather than a terminal transcript.
type OutputLine struct {
	Stream OutputStream
	Text   string
}

// RunForegroundTyped executes an isolated command while preserving stdout and
// stderr. onStart runs synchronously after the process starts, before output is
// consumed. onLine may be invoked concurrently for the two streams and must
// return promptly.
func (m *Manager) RunForegroundTyped(ctx context.Context, b *Bed, command, cwdInBed string, envs map[string]string, timeout time.Duration, onStart func(), onLine func(OutputLine)) (int, error) {
	finishOperation, err := m.BeginOperation(b, OpExec, timeout)
	if err != nil {
		return -1, err
	}
	defer finishOperation()
	cmd, err := m.buildCommand(b, command, cwdInBed, envs)
	if err != nil {
		return -1, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return -1, err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdoutR.Close()
		stdoutW.Close()
		return -1, err
	}
	cmd.Stdout, cmd.Stderr = stdoutW, stderrW
	proc, err := m.spawner.Start(b.ID, cmd)
	stdoutW.Close()
	stderrW.Close()
	if err != nil {
		stdoutR.Close()
		stderrR.Close()
		return -1, err
	}
	start := time.Now()
	if timeout > 0 {
		t := time.AfterFunc(timeout, proc.Kill)
		defer t.Stop()
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			proc.Kill()
		case <-stop:
		}
	}()
	if onStart != nil {
		onStart()
	}

	var wg sync.WaitGroup
	read := func(stream OutputStream, file *os.File) {
		defer wg.Done()
		defer file.Close()
		// ReadString keeps the child's bytes intact, including whether the final
		// fragment had a newline. Scanner would strip delimiters and force us to
		// invent one, making exec results differ from a direct process capture.
		reader := bufio.NewReader(file)
		for {
			text, readErr := reader.ReadString('\n')
			if text != "" && onLine != nil {
				onLine(OutputLine{Stream: stream, Text: text})
			}
			if readErr != nil {
				return
			}
		}
	}
	// Drain both pipes concurrently: reading either one serially can deadlock
	// once a child fills the other pipe's kernel buffer.
	wg.Add(2)
	go read(StreamStdout, stdoutR)
	go read(StreamStderr, stderrR)
	type waitResult struct {
		code int
		err  error
	}
	waitResultCh := make(chan waitResult, 1)
	go func() {
		code, waitErr := proc.Wait()
		waitResultCh <- waitResult{code: code, err: waitErr}
	}()
	wg.Wait()
	result := <-waitResultCh
	b.RecordCommand(time.Since(start))
	if result.err != nil {
		return -1, result.err
	}
	return result.code, nil
}
