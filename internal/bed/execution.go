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
	"sync"
	"time"

	"github.com/qiankunli/go-stdx/randx"
	"github.com/qiankunli/hostel/internal/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type ExecutionMode string

const (
	ExecutionForeground ExecutionMode = "foreground"
	ExecutionBackground ExecutionMode = "background"
	ExecutionSession    ExecutionMode = "session"
)

type TerminationCause string

const (
	CauseNatural        TerminationCause = "natural"
	CauseTimeout        TerminationCause = "timeout"
	CauseClientCanceled TerminationCause = "client_canceled"
	CauseInterrupted    TerminationCause = "interrupted"
	CauseBedTeardown    TerminationCause = "bed_teardown"
	CauseDaemonShutdown TerminationCause = "daemon_shutdown"
	CauseExternalSignal TerminationCause = "external_signal"
	CauseOOM            TerminationCause = "oom"
	CauseRuntimeLost    TerminationCause = "runtime_lost"
)

// OutputStream identifies one side of an execution's output.
type OutputStream string

const (
	StreamStdout OutputStream = "stdout"
	StreamStderr OutputStream = "stderr"
)

// ExecutionOutput is one ordered output fragment. Sequence remains monotonic
// when older buffered fragments are evicted, so cursor consumers can resume
// without confusing truncation with a fresh execution.
type ExecutionOutput struct {
	Sequence int64
	Stream   OutputStream
	Text     string
}

// ExecutionResult combines the process fact owned by the spawner with the
// termination intent owned by the execution controller.
type ExecutionResult struct {
	ExecutionID string
	BedID       string
	Mode        ExecutionMode
	Spawner     string
	StartedAt   time.Time
	FinishedAt  time.Time
	Duration    time.Duration
	Process     ProcessOutcome
	Cause       TerminationCause
}

type ExecutionStatus struct {
	ID         string
	BedID      string
	Mode       ExecutionMode
	Spawner    string
	Running    bool
	StartedAt  time.Time
	FinishedAt *time.Time
	Result     *ExecutionResult
}

// Execution is one one-shot command lifetime. Foreground and background are
// the same object; mode only decides whether the initiating HTTP request waits.
type Execution struct {
	ID      string
	BedID   string
	Mode    ExecutionMode
	Spawner string

	mu          sync.Mutex
	ctx         context.Context
	span        oteltrace.Span
	stop        func()
	stopDone    chan struct{}
	startedAt   time.Time
	finishedAt  *time.Time
	result      *ExecutionResult
	stopCause   TerminationCause
	output      []ExecutionOutput
	outputBytes int
	truncated   bool
	nextOutput  int64
	dropped     int64
	done        chan struct{}
}

const (
	executionOutputBytes     = 64 << 10
	executionOutputFragments = 1_024
	executionHistoryLimit    = 1_024
	executionDrainGrace      = 100 * time.Millisecond
)

func newExecution(ctx context.Context, bedID string, mode ExecutionMode, spawner string, stop func()) *Execution {
	if ctx == nil {
		ctx = context.Background()
	}
	executionID := "exec-" + randx.Hex(8)
	ctx, span := tracing.Tracer().Start(ctx, "hostel.execution", oteltrace.WithAttributes(
		attribute.String("hostel.execution.id", executionID),
		attribute.String("hostel.bed.id", bedID),
		attribute.String("hostel.execution.mode", string(mode)),
		attribute.String("hostel.execution.spawner", spawner),
	))
	return &Execution{
		ID:        executionID,
		BedID:     bedID,
		Mode:      mode,
		Spawner:   spawner,
		ctx:       ctx,
		span:      span,
		stop:      stop,
		stopDone:  make(chan struct{}),
		startedAt: time.Now(),
		done:      make(chan struct{}),
	}
}

// RequestStop records the first actor that decided the execution must end,
// then kills the process tree. Recording happens before signalling so Wait can
// never observe SIGKILL and lose the initiating cause.
func (e *Execution) RequestStop(cause TerminationCause) bool {
	e.mu.Lock()
	if e.result != nil || e.stopCause != "" {
		e.mu.Unlock()
		return false
	}
	e.stopCause = cause
	stop := e.stop
	e.mu.Unlock()
	if stop != nil {
		stop()
	}
	close(e.stopDone)
	return true
}

func (e *Execution) appendOutput(stream OutputStream, text string) ExecutionOutput {
	e.mu.Lock()
	defer e.mu.Unlock()
	output := ExecutionOutput{Sequence: e.nextOutput, Stream: stream, Text: text}
	e.nextOutput++
	retained := output
	if len(retained.Text) > executionOutputBytes {
		retained.Text = retained.Text[len(retained.Text)-executionOutputBytes:]
		e.truncated = true
	}
	e.output = append(e.output, retained)
	e.outputBytes += len(retained.Text)
	for e.outputBytes > executionOutputBytes || len(e.output) > executionOutputFragments {
		e.outputBytes -= len(e.output[0].Text)
		e.output = e.output[1:]
		e.dropped++
		e.truncated = true
	}
	return output
}

func (e *Execution) finish(outcome ProcessOutcome, onFinish func(ExecutionResult)) ExecutionResult {
	finishedAt := time.Now()
	e.mu.Lock()
	cause := e.stopCause
	stopDone := e.stopDone
	if cause != "" {
		e.mu.Unlock()
		// Terminal publication must not race the stop action that produced it.
		// In particular, a session's shell may observe EOF before Kill has
		// finished serializing the process-group signal.
		<-stopDone
		e.mu.Lock()
	}
	if cause == "" {
		switch outcome.Kind {
		case ProcessExited:
			cause = CauseNatural
		case ProcessSignaled:
			cause = CauseExternalSignal
		default:
			cause = CauseRuntimeLost
		}
	}
	result := ExecutionResult{
		ExecutionID: e.ID,
		BedID:       e.BedID,
		Mode:        e.Mode,
		Spawner:     e.Spawner,
		StartedAt:   e.startedAt,
		FinishedAt:  finishedAt,
		Duration:    finishedAt.Sub(e.startedAt),
		Process:     outcome,
		Cause:       cause,
	}
	e.finishedAt = &finishedAt
	e.result = &result
	e.mu.Unlock()
	if onFinish != nil {
		onFinish(result)
	}
	e.span.SetAttributes(
		attribute.String("hostel.execution.process.outcome", string(result.Process.Kind)),
		attribute.String("hostel.execution.termination_cause", string(result.Cause)),
		attribute.Int64("hostel.execution.duration_ms", result.Duration.Milliseconds()),
	)

	attrs := []any{
		"execution_id", result.ExecutionID,
		"bed", result.BedID,
		"mode", result.Mode,
		"spawner", result.Spawner,
		"outcome", result.Process.Kind,
		"cause", result.Cause,
		"duration_ms", result.Duration.Milliseconds(),
	}
	switch result.Process.Kind {
	case ProcessExited:
		e.span.SetAttributes(attribute.Int("hostel.execution.exit_code", result.Process.ExitCode))
		attrs = append(attrs, "exit_code", result.Process.ExitCode)
		if result.Process.ExitCode != 0 {
			e.span.SetStatus(codes.Error, "command exited non-zero")
		}
	case ProcessSignaled:
		e.span.SetAttributes(
			attribute.Int("hostel.execution.signal", result.Process.Signal),
			attribute.Bool("hostel.execution.core_dumped", result.Process.CoreDumped),
		)
		attrs = append(attrs, "signal", result.Process.Signal, "core_dumped", result.Process.CoreDumped)
		if result.Cause != CauseClientCanceled && result.Cause != CauseInterrupted &&
			result.Cause != CauseBedTeardown && result.Cause != CauseDaemonShutdown {
			e.span.SetStatus(codes.Error, "command terminated by signal")
		}
	case ProcessLost:
		e.span.RecordError(fmt.Errorf("process lost: %s", result.Process.Error))
		e.span.SetStatus(codes.Error, "process runtime lost")
		attrs = append(attrs, "error", result.Process.Error)
	}
	tracing.InfoContext(e.ctx, "hostel execution finished", attrs...)
	e.span.End()
	close(e.done)
	return result
}

func (e *Execution) Wait() ExecutionResult {
	<-e.done
	e.mu.Lock()
	defer e.mu.Unlock()
	return *e.result
}

func (e *Execution) Status() ExecutionStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	status := ExecutionStatus{
		ID:        e.ID,
		BedID:     e.BedID,
		Mode:      e.Mode,
		Spawner:   e.Spawner,
		Running:   e.result == nil,
		StartedAt: e.startedAt,
	}
	if e.finishedAt != nil {
		finishedAt := *e.finishedAt
		status.FinishedAt = &finishedAt
	}
	if e.result != nil {
		result := *e.result
		status.Result = &result
	}
	return status
}

// Logs returns retained output after cursor. The cursor is an absolute output
// sequence. Truncated reports that retained history is incomplete, including
// a cursor gap or a single fragment larger than the retention byte budget.
func (e *Execution) Logs(cursor int64) (output []ExecutionOutput, next int64, running, truncated bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	start := cursor + 1
	if cursor < 0 {
		start = 0
	}
	if start < e.dropped {
		start = e.dropped
		truncated = true
	}
	truncated = truncated || e.truncated
	if start < e.nextOutput {
		output = append([]ExecutionOutput(nil), e.output[start-e.dropped:]...)
	}
	next = e.nextOutput - 1
	return output, next, e.result == nil, truncated
}

type ExecutionRegistry struct {
	mu         sync.Mutex
	executions map[string]*Execution
	order      []string
}

func newExecutionRegistry() *ExecutionRegistry {
	return &ExecutionRegistry{executions: make(map[string]*Execution)}
}

func (r *ExecutionRegistry) track(
	ctx context.Context,
	bedID string,
	mode ExecutionMode,
	spawner string,
	proc Proc,
	stdout, stderr io.ReadCloser,
	timeout time.Duration,
	onStart func(ExecutionStatus),
	onOutput func(ExecutionOutput),
	onFinish func(ExecutionResult),
) *Execution {
	execution := newExecution(ctx, bedID, mode, spawner, proc.Kill)
	r.mu.Lock()
	r.executions[execution.ID] = execution
	r.order = append(r.order, execution.ID)
	r.mu.Unlock()
	tracing.InfoContext(execution.ctx, "hostel execution started",
		"execution_id", execution.ID,
		"bed", execution.BedID,
		"mode", execution.Mode,
		"spawner", execution.Spawner,
	)

	if onStart != nil {
		onStart(execution.Status())
	}
	var timeoutTimer *time.Timer
	if timeout > 0 {
		timeoutTimer = time.AfterFunc(timeout, func() { execution.RequestStop(CauseTimeout) })
	}
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				execution.RequestStop(contextTerminationCause(ctx))
			case <-execution.done:
			}
		}()
	}

	go func() {
		var wg sync.WaitGroup
		drain := func(stream OutputStream, input io.ReadCloser) {
			defer wg.Done()
			defer input.Close()
			reader := bufio.NewReader(input)
			for {
				text, err := reader.ReadString('\n')
				if text != "" {
					output := execution.appendOutput(stream, text)
					if onOutput != nil {
						onOutput(output)
					}
				}
				if err != nil {
					return
				}
			}
		}
		wg.Add(2)
		go drain(StreamStdout, stdout)
		go drain(StreamStderr, stderr)
		outcome := proc.Wait()
		if timeoutTimer != nil {
			timeoutTimer.Stop()
		}
		drained := make(chan struct{})
		go func() {
			wg.Wait()
			close(drained)
		}()
		select {
		case <-drained:
		case <-time.After(executionDrainGrace):
			// A descendant may outlive the command leader while retaining inherited
			// output fds. Terminal publication must not wait forever for that leak.
			stdout.Close()
			stderr.Close()
			<-drained
		}
		execution.finish(outcome, onFinish)
		r.prune()
	}()
	return execution
}

// trackSession projects one command run inside a stateful shell into the same
// execution model. Its process outcome is the shell marker status; canceling a
// run closes the session so no command can continue after a terminal record.
func (r *ExecutionRegistry) trackSession(
	ctx context.Context,
	bedID string,
	shell *Shell,
	command string,
	timeout time.Duration,
	onStart func(ExecutionStatus),
	onOutput func(ExecutionOutput),
	onFinish func(ExecutionResult),
) *Execution {
	execution := newExecution(ctx, bedID, ExecutionSession, "session_shell", shell.Close)
	r.mu.Lock()
	r.executions[execution.ID] = execution
	r.order = append(r.order, execution.ID)
	r.mu.Unlock()
	tracing.InfoContext(execution.ctx, "hostel execution started",
		"execution_id", execution.ID,
		"bed", execution.BedID,
		"mode", execution.Mode,
		"spawner", execution.Spawner,
	)

	if onStart != nil {
		onStart(execution.Status())
	}
	var timeoutTimer *time.Timer
	if timeout > 0 {
		timeoutTimer = time.AfterFunc(timeout, func() { execution.RequestStop(CauseTimeout) })
	}
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				execution.RequestStop(contextTerminationCause(ctx))
			case <-execution.done:
			}
		}()
	}

	go func() {
		result, err := shell.Run(execution.ctx, command, func(text string) {
			output := execution.appendOutput(StreamStdout, text)
			if onOutput != nil {
				onOutput(output)
			}
		})
		outcome := ProcessOutcome{}
		if err != nil {
			outcome = lostProcess(err)
		} else {
			outcome = exitedProcess(result.ExitCode)
		}
		if timeoutTimer != nil {
			timeoutTimer.Stop()
		}
		execution.finish(outcome, onFinish)
		r.prune()
	}()
	return execution
}

func contextTerminationCause(ctx context.Context) TerminationCause {
	if ctx.Err() == context.DeadlineExceeded {
		return CauseTimeout
	}
	return CauseClientCanceled
}

func (r *ExecutionRegistry) Get(id string) (*Execution, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	execution, ok := r.executions[id]
	return execution, ok
}

func (r *ExecutionRegistry) killBed(bedID string, cause TerminationCause) {
	r.mu.Lock()
	var executions []*Execution
	for _, execution := range r.executions {
		if execution.BedID == bedID {
			executions = append(executions, execution)
		}
	}
	r.mu.Unlock()
	for _, execution := range executions {
		execution.RequestStop(cause)
	}
}

func (r *ExecutionRegistry) prune() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.executions) <= executionHistoryLimit {
		return
	}
	kept := r.order[:0]
	for _, id := range r.order {
		execution, ok := r.executions[id]
		if !ok {
			continue
		}
		if len(r.executions) > executionHistoryLimit && !execution.Status().Running {
			delete(r.executions, id)
			continue
		}
		kept = append(kept, id)
	}
	r.order = kept
}
