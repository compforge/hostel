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
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/qiankunli/go-stdx/randx"
	"github.com/qiankunli/go-stdx/shellx"

	"github.com/qiankunli/hostel/internal/bedfs"
	"github.com/qiankunli/hostel/internal/executor"
	"github.com/qiankunli/hostel/internal/isolation"
)

// resolveShellPath prefers the configured shell but degrades to sh for
// minimal images where bash is absent. The resolved path is shared by both
// one-shot commands and stateful sessions so their syntax contract matches.
func resolveShellPath(configured string) string {
	if configured != "" {
		if path, err := exec.LookPath(configured); err == nil {
			return path
		}
	}
	if path, err := exec.LookPath("sh"); err == nil {
		return path
	}
	return configured
}

func shellCommandArgs(shellPath, command string) []string {
	if filepath.Base(shellPath) == "bash" {
		return []string{"--noprofile", "--norc", "-c", command}
	}
	return []string{"-c", command}
}

var bedIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func validBedID(id string) error {
	if !bedIDRe.MatchString(id) {
		return fmt.Errorf("bed: invalid id %q (allowed: alnum . _ -, ≤128)", id)
	}
	return nil
}

// Shell is a bed's long-running shell process. Commands are written to its
// stdin and framed by a per-run marker so stateful shell context (cwd, env,
// functions) persists across runs — matching "shell state survives across
// exec" semantics. A single reader goroutine drains combined stdout/stderr into
// lines; Run (serialized by runMu) is the sole consumer, so output can't leak
// between runs.
//
// LOCKING (the original single-mutex design deadlocked a live daemon — see
// the fix commit): runMu is held for a Run's whole duration and is touched by
// NOBODY else; mu guards only the dead flag and is held for nanoseconds. The
// reader goroutine needs mu (never runMu) to mark death before closing lines,
// so a dying shell can always unblock the Run that is waiting on the channel.
// Callers holding bed/manager locks may call Dead() safely for the same
// reason. Never add code that holds mu while blocking.
type Shell struct {
	ExecutorID      string
	ExecutorBackend string

	proc  executor.Process
	stdin io.WriteCloser
	lines chan string // every output line; closed on EOF/exit

	runMu sync.Mutex // serializes Run; held while waiting for output
	mu    sync.Mutex // guards dead only; held briefly
	dead  bool
}

// startShell launches the shell in the Bed's current Executor. cwdInBed,
// when set, becomes the starting directory via an initial `cd`. env is the
// bed-scoped environment (Manager.buildBedEnv) — the session shell would otherwise
// inherit the daemon env, which lacks the bed identity and endpoints. Stdio is
// explicit os.Pipe pairs (not StdinPipe/StdoutPipe) so the raw fds can cross a
// process boundary when supervisor is the Executor backend.
func startShell(bedExecutor executor.Executor, shellPath string, env []string, iso isolation.Isolator, fs *bedfs.FS, cwdInBed string) (*Shell, error) {
	cmd := exec.Command(shellPath, shellInteractiveArgs(shellPath)...)
	cmd.Env = env
	if err := iso.Wrap(cmd, fs); err != nil {
		return nil, err
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	cmd.Stdin = inR
	cmd.Stdout = outW
	cmd.Stderr = outW // interleave, like a terminal
	proc, err := bedExecutor.Start(context.Background(), "process-shell-"+randx.Hex(8), cmd)
	// The child holds its own copies now (or never will, on error): drop ours
	// of the child-side ends — outW in particular, or the reader never EOFs.
	inR.Close()
	outW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		return nil, err
	}

	s := &Shell{
		ExecutorID:      bedExecutor.ID(),
		ExecutorBackend: bedExecutor.Backend(),
		proc:            proc,
		stdin:           inW,
		lines:           make(chan string, 64),
	}
	if cwdInBed != "" {
		// Best-effort initial cwd; a failure surfaces in the first run's output.
		_, _ = io.WriteString(inW, "cd -- "+shellx.Quote(cwdInBed)+" || true\n")
	}
	// Single long-lived reader → lines channel.
	go func() {
		r := bufio.NewReader(outR)
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				s.lines <- line
			}
			if err != nil {
				outR.Close()
				s.mu.Lock()
				s.dead = true
				s.mu.Unlock()
				close(s.lines)
				return
			}
		}
	}()
	go func() { _, _ = proc.Wait(context.Background()) }() // EOF above drives dead state
	return s, nil
}

func shellInteractiveArgs(shellPath string) []string {
	if filepath.Base(shellPath) == "bash" {
		return []string{"--noprofile", "--norc"}
	}
	return nil
}

// Dead reports whether the shell process has exited.
func (s *Shell) Dead() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dead
}

// Close terminates the shell process group.
func (s *Shell) Close() {
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.proc != nil {
		s.proc.Kill()
	}
}

// RunResult is the outcome of one command in the shell.
type RunResult struct {
	ExitCode int
}

// Run writes command to the shell and streams combined stdout/stderr to onLine
// until the command completes, detected by a unique end-marker echoing $?.
// ctx cancels the wait (the shell keeps running; caller may Close to abort).
//
// +spec=`Command output is preserved whether or not its final fragment ends with a newline; the framing marker is never exposed as output.`
// +case:id=session_partial_line_output,desc=`Run printf without a trailing newline in a persistent shell`,expect=`the partial output is returned and the execution reaches its typed terminal result`
func (s *Shell) Run(ctx context.Context, command string, onLine func(string)) (*RunResult, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.Dead() {
		return nil, fmt.Errorf("shell: session is dead")
	}

	marker := fmt.Sprintf("__hostel_end_%d__", time.Now().UnixNano())
	full := fmt.Sprintf("%s\nprintf '%%s %%d\\n' %s \"$?\"\n", command, marker)
	if _, err := io.WriteString(s.stdin, full); err != nil {
		return nil, fmt.Errorf("shell: write command: %w", err)
	}
	markerRe := regexp.MustCompile(regexp.QuoteMeta(marker) + ` (\d+)\s*$`)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case line, ok := <-s.lines:
			if !ok {
				return nil, fmt.Errorf("shell: session exited during run")
			}
			if match := markerRe.FindStringSubmatchIndex(line); match != nil {
				if prefix := line[:match[0]]; prefix != "" && onLine != nil {
					onLine(prefix)
				}
				code := 0
				fmt.Sscanf(line[match[2]:match[3]], "%d", &code)
				return &RunResult{ExitCode: code}, nil
			}
			if onLine != nil {
				onLine(line)
			}
		}
	}
}

// CreateShell starts a new stateful shell session in the bed and returns its id.
// cwdInBed, when non-empty, is the starting directory (already resolved+confined
// by the caller via BedFS).
func (m *Manager) CreateShell(b *Bed, cwdInBed string) (string, error) {
	m.touchBed(b)
	env, err := m.buildBedEnv(b, nil)
	if err != nil {
		return "", err
	}
	bedExecutor, err := b.executorFor(context.Background(), m.executorFactory)
	if err != nil {
		return "", err
	}
	sh, err := startShell(bedExecutor, m.shellPath, env, m.iso, b.BedFS(), cwdInBed)
	if err != nil {
		return "", err
	}
	id := "session-" + randx.Hex(8)
	b.mu.Lock()
	b.shells[id] = sh
	b.mu.Unlock()
	return id, nil
}

// StartSessionExecution runs one command in a stateful shell while exposing
// the same identity, output and terminal record as one-shot executions.
func (m *Manager) StartSessionExecution(
	ctx context.Context,
	b *Bed,
	shell *Shell,
	command string,
	timeout time.Duration,
	onStart func(ExecutionStatus),
	onOutput func(ExecutionOutput),
) (*Execution, error) {
	finishOperation, err := m.BeginOperation(b, OpExec, timeout)
	if err != nil {
		return nil, err
	}
	execution := m.executions.trackSession(ctx, b.ID, shell, command, timeout, onStart, onOutput, func(result ExecutionResult) {
		finishOperation()
		b.RecordCommand(result.Duration)
	})
	return execution, nil
}

// foregroundShellID is the well-known session backing the explicit /session
// foreground shell (cwd/env persist across its runs). NOTE: the one-shot
// /command path no longer uses it — foreground /command now runs a fresh
// isolated process (see Manager.RunForeground). Retained for /session and its
// tests; a candidate for removal once /session is reworked.
const foregroundShellID = "session-foreground"

// ForegroundShell returns the bed's implicit foreground shell, starting it
// once and reusing it (restarting if it died).
func (m *Manager) ForegroundShell(b *Bed) (*Shell, error) {
	m.touchBed(b)
	b.mu.Lock()
	if sh, ok := b.shells[foregroundShellID]; ok && !sh.Dead() {
		b.mu.Unlock()
		return sh, nil
	}
	b.mu.Unlock()

	env, err := m.buildBedEnv(b, nil)
	if err != nil {
		return nil, err
	}
	bedExecutor, err := b.executorFor(context.Background(), m.executorFactory)
	if err != nil {
		return nil, err
	}
	sh, err := startShell(bedExecutor, m.shellPath, env, m.iso, b.BedFS(), "")
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if ex, ok := b.shells[foregroundShellID]; ok && !ex.Dead() { // lost a race
		sh.Close()
		return ex, nil
	}
	b.shells[foregroundShellID] = sh
	return sh, nil
}

// GetShell returns a live shell session by id.
func (b *Bed) GetShell(id string) (*Shell, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sh, ok := b.shells[id]
	if !ok || sh.Dead() {
		return nil, false
	}
	return sh, true
}

// DeleteShell kills and removes a shell session. Returns false when unknown.
func (b *Bed) DeleteShell(id string) bool {
	b.mu.Lock()
	sh, ok := b.shells[id]
	if ok {
		delete(b.shells, id)
	}
	b.mu.Unlock()
	if ok {
		sh.Close()
	}
	return ok
}
