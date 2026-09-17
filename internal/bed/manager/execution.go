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
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/qiankunli/go-stdx/randx"
	hostel "github.com/qiankunli/hostel/internal"
	"github.com/qiankunli/hostel/internal/bed/executor"
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
	ExecutionService    ExecutionMode = "service"
)

type TerminationCause string

const (
	CausePreparationFailed TerminationCause = "preparation_failed"
	CauseOutputFailure     TerminationCause = "output_failure"
	CauseNatural           TerminationCause = "natural"
	CauseTimeout           TerminationCause = "timeout"
	CauseClientCanceled    TerminationCause = "client_canceled"
	CauseInterrupted       TerminationCause = "interrupted"
	CauseServiceStop       TerminationCause = "service_stop"
	CauseBedTeardown       TerminationCause = "bed_teardown"
	CauseDaemonShutdown    TerminationCause = "daemon_shutdown"
	CauseExternalSignal    TerminationCause = "external_signal"
	CauseOOM               TerminationCause = "oom"
	CauseExecutorLost      TerminationCause = "executor_lost"
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

// ExecutionResult combines the process fact owned by the Executor with the
// termination intent owned by the execution controller.
type ExecutionResult struct {
	ExecutionID     string
	BedID           string
	Mode            ExecutionMode
	ExecutorID      string
	ExecutorBackend string
	StartedAt       time.Time
	FinishedAt      time.Time
	Duration        time.Duration
	// Process is nil when the command did not start; Err preserves the preparation failure.
	Process *executor.ProcessOutcome
	Err     error
	Cause   TerminationCause
}

type ExecutionStatus struct {
	ID              string
	BedID           string
	Mode            ExecutionMode
	ExecutorID      string
	ExecutorBackend string
	Running         bool
	StartedAt       time.Time
	FinishedAt      *time.Time
	Result          *ExecutionResult
}

// Execution is one one-shot command lifetime. Foreground and background are
// the same object; mode only decides whether the initiating HTTP request waits.
type Execution struct {
	Content string
	log     *executionLog

	ID              string
	BedID           string
	Mode            ExecutionMode
	ExecutorID      string
	ExecutorBackend string

	mu          sync.Mutex
	ctx         context.Context
	span        oteltrace.Span
	stop        func()
	stopDone    chan struct{}
	finishing   bool
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

func newExecution(ctx context.Context, bedID string, mode ExecutionMode, executorID, executorBackend string, stop func()) *Execution {
	if ctx == nil {
		ctx = context.Background()
	}
	executionID := "exec-" + randx.Hex(8)
	ctx, span := tracing.Tracer().Start(ctx, "hostel.execution", oteltrace.WithAttributes(
		attribute.String("hostel.execution.id", executionID),
		attribute.String("hostel.bed.id", bedID),
		attribute.String("hostel.execution.mode", string(mode)),
		attribute.String("hostel.executor.id", executorID),
		attribute.String("hostel.executor.backend", executorBackend),
	))
	return &Execution{
		ID:              executionID,
		BedID:           bedID,
		Mode:            mode,
		ExecutorID:      executorID,
		ExecutorBackend: executorBackend,
		ctx:             ctx,
		span:            span,
		stop:            stop,
		stopDone:        make(chan struct{}),
		startedAt:       time.Now(),
		done:            make(chan struct{}),
	}
}

// RequestStop records the first actor that decided the execution must end,
// then kills the process tree. Recording happens before signalling so Wait can
// never observe SIGKILL and lose the initiating cause.
func (e *Execution) RequestStop(cause TerminationCause) bool {
	return e.requestStop(cause, e.stop)
}

func (e *Execution) requestStop(cause TerminationCause, stop func()) bool {
	e.mu.Lock()
	if e.result != nil || e.finishing || e.stopCause != "" {
		e.mu.Unlock()
		return false
	}
	e.stopCause = cause
	e.mu.Unlock()
	if stop != nil {
		stop()
	}
	close(e.stopDone)
	return true
}

// claimFinish linearizes natural process completion against stop requests.
// Whichever transition acquires e.mu first owns the terminal cause: a timer
// cannot kill a persistent session after its command has already completed.
func (e *Execution) claimFinish() (TerminationCause, <-chan struct{}) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopCause == "" {
		e.finishing = true
		return "", nil
	}
	return e.stopCause, e.stopDone
}

func (e *Execution) appendOutput(stream OutputStream, text string) ExecutionOutput {
	e.mu.Lock()
	defer e.mu.Unlock()
	output := ExecutionOutput{Sequence: e.nextOutput, Stream: stream, Text: text}
	e.nextOutput++

	retained := output
	if len(retained.Text) > executionOutputBytes {
		retained.Text = strings.Clone(retained.Text[len(retained.Text)-executionOutputBytes:])
		e.truncated = true
	}
	e.output = append(e.output, retained)
	e.outputBytes += len(retained.Text)
	for e.outputBytes > executionOutputBytes || len(e.output) > executionOutputFragments {
		e.outputBytes -= len(e.output[0].Text)
		e.output[0] = ExecutionOutput{} // Release text still referenced by the backing array.
		e.output = e.output[1:]
		e.dropped++
		e.truncated = true
	}
	return output
}

func (e *Execution) finish(outcome executor.ProcessOutcome, onFinish func(ExecutionResult)) ExecutionResult {
	return e.finishResult(&outcome, nil, onFinish)
}

func (e *Execution) finishResult(outcome *executor.ProcessOutcome, failure error, onFinish func(ExecutionResult)) ExecutionResult {
	finishedAt := time.Now()
	cause, stopDone := e.claimFinish()
	if stopDone != nil {
		// Terminal publication must not race the stop action that produced it.
		// In particular, a session's shell may observe EOF before Kill has
		// finished serializing the process-group signal.
		<-stopDone
	}
	if cause == "" && failure != nil {
		cause = CausePreparationFailed
	}
	if cause == "" {
		switch outcome.Kind {
		case executor.ProcessExited:
			cause = CauseNatural
		case executor.ProcessSignaled:
			cause = CauseExternalSignal
		default:
			cause = CauseExecutorLost
		}
	}
	e.mu.Lock()
	result := ExecutionResult{
		ExecutionID:     e.ID,
		BedID:           e.BedID,
		Mode:            e.Mode,
		ExecutorID:      e.ExecutorID,
		ExecutorBackend: e.ExecutorBackend,
		StartedAt:       e.startedAt,
		FinishedAt:      finishedAt,
		Duration:        finishedAt.Sub(e.startedAt),
		Process:         outcome,
		Err:             failure,
		Cause:           cause,
	}
	e.finishedAt = &finishedAt
	if e.log != nil {
		if err := e.log.writer.Close(); err != nil && e.log.err == nil {
			e.log.err = err
		}
		e.log.writer = nil
	}
	e.result = &result
	e.finishing = false
	e.mu.Unlock()
	if onFinish != nil {
		onFinish(result)
	}
	processKind := "not_started"
	if outcome != nil {
		processKind = string(outcome.Kind)
	}
	e.span.SetAttributes(
		attribute.String("hostel.execution.process.outcome", processKind),
		attribute.String("hostel.execution.termination_cause", string(result.Cause)),
		attribute.Int64("hostel.execution.duration_ms", result.Duration.Milliseconds()),
	)

	attrs := []any{
		"execution_id", result.ExecutionID,
		"bed", result.BedID,
		"mode", result.Mode,
		"executor_id", result.ExecutorID,
		"executor_backend", result.ExecutorBackend,
		"outcome", processKind,
		"cause", result.Cause,
		"duration_ms", result.Duration.Milliseconds(),
	}
	if failure != nil {
		e.span.SetStatus(codes.Error, "execution preparation failed")
		attrs = append(attrs, "error", "execution preparation failed")
	}
	switch executor.ProcessOutcomeKind(processKind) {
	case executor.ProcessExited:
		e.span.SetAttributes(attribute.Int("hostel.execution.exit_code", result.Process.ExitCode))
		attrs = append(attrs, "exit_code", result.Process.ExitCode)
		if result.Process.ExitCode != 0 {
			e.span.SetStatus(codes.Error, "command exited non-zero")
		}
	case executor.ProcessSignaled:
		e.span.SetAttributes(
			attribute.Int("hostel.execution.signal", result.Process.Signal),
			attribute.Bool("hostel.execution.core_dumped", result.Process.CoreDumped),
		)
		attrs = append(attrs, "signal", result.Process.Signal, "core_dumped", result.Process.CoreDumped)
		if result.Cause != CauseClientCanceled && result.Cause != CauseInterrupted &&
			result.Cause != CauseBedTeardown && result.Cause != CauseDaemonShutdown {
			e.span.SetStatus(codes.Error, "command terminated by signal")
		}
	case executor.ProcessLost:
		detail := result.Process.Detail
		if detail == "" {
			detail = result.Process.Error
		}
		e.span.RecordError(fmt.Errorf("process lost: %s", detail))
		e.span.SetStatus(codes.Error, "executor lost")
		attrs = append(attrs, "error", result.Process.Error, "error_detail", detail)
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
		ID:              e.ID,
		BedID:           e.BedID,
		Mode:            e.Mode,
		ExecutorID:      e.ExecutorID,
		ExecutorBackend: e.ExecutorBackend,
		Running:         e.result == nil,
		StartedAt:       e.startedAt,
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
	executorID, executorBackend string,
	proc executor.Process,
	stdout, stderr io.ReadCloser,
	timeout time.Duration,
	content string, outputLog *executionLog,
	onStart func(ExecutionStatus),
	onOutput func(ExecutionOutput),
	onFinish func(ExecutionResult),
) *Execution {
	execution := newExecution(ctx, bedID, mode, executorID, executorBackend, proc.Kill)
	execution.Content, execution.log = content, outputLog
	r.mu.Lock()
	r.executions[execution.ID] = execution
	r.order = append(r.order, execution.ID)
	r.mu.Unlock()
	tracing.InfoContext(execution.ctx, "hostel execution started",
		"execution_id", execution.ID,
		"bed", execution.BedID,
		"mode", execution.Mode,
		"executor_id", execution.ExecutorID,
		"executor_backend", execution.ExecutorBackend,
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
			var source io.Reader = input
			if execution.log != nil {
				source = io.TeeReader(input, executionLogWriter{execution})
			}
			reader := bufio.NewReader(source)
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
		// Retain trace identity for transport retry events without letting an HTTP
		// cancellation make Wait abandon the process before its terminal fact.
		outcome, waitErr := proc.Wait(context.WithoutCancel(execution.ctx))
		if waitErr != nil {
			outcome = executor.Lost(execution.ExecutorID, waitErr)
		}
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
	cwdInBed string,
	settings *SessionSettings,
	timeout time.Duration,
	onStart func(ExecutionStatus),
	onOutput func(ExecutionOutput),
	onFinish func(ExecutionResult),
) *Execution {
	execution := newExecution(ctx, bedID, ExecutionSession, shell.ExecutorID, shell.ExecutorBackend, shell.Close)
	r.mu.Lock()
	r.executions[execution.ID] = execution
	r.order = append(r.order, execution.ID)
	r.mu.Unlock()
	tracing.InfoContext(execution.ctx, "hostel execution started",
		"execution_id", execution.ID,
		"bed", execution.BedID,
		"mode", execution.Mode,
		"executor_id", execution.ExecutorID,
		"executor_backend", execution.ExecutorBackend,
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
		result, err := shell.runAt(execution.ctx, cwdInBed, command, settings, func(text string) {
			output := execution.appendOutput(StreamStdout, text)
			if onOutput != nil {
				onOutput(output)
			}
		})
		if timeoutTimer != nil {
			timeoutTimer.Stop()
		}
		if errors.Is(err, hostel.ErrPreparationFailed) {
			execution.finishResult(nil, err, onFinish)
		} else if err != nil {
			execution.finish(executor.Lost(shell.ExecutorID, err), onFinish)
		} else {
			execution.finish(executor.Exited(result.ExitCode), onFinish)
		}
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
		if execution.BedID == bedID && execution.Mode != ExecutionService {
			executions = append(executions, execution)
		}
	}
	r.mu.Unlock()
	for _, execution := range executions {
		execution.RequestStop(cause)
	}
}

// prune retains metadata/disk logs independently from the bounded hot output
// cache. Running executions are never evicted. Old native fragment cursors
// explicitly report truncation after their cache is released.
func (r *ExecutionRegistry) prune() {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := make([]string, 0, len(r.order))
	nativeRecords, hotRecords := 0, 0
	for i := len(r.order) - 1; i >= 0; i-- {
		id := r.order[i]
		e, ok := r.executions[id]
		if !ok {
			continue
		}
		status := e.Status()
		if !status.Running {
			expired := status.FinishedAt != nil && time.Since(*status.FinishedAt) >= 24*time.Hour
			if e.Mode != ExecutionBackground && !expired {
				nativeRecords++
			}
			evict := expired || (e.Mode != ExecutionBackground && nativeRecords > executionHistoryLimit)
			if evict {
				if err := e.removeLog(); err == nil {
					delete(r.executions, id)
					continue
				}
				// Keep failed cleanup in the index so a later collection can retry.
			}
			hotRecords++
			if hotRecords > executionHistoryLimit {
				e.releaseOutput()
			}
		}
		kept = append(kept, id)
	}
	slices.Reverse(kept)
	r.order = kept
}

func (e *Execution) releaseOutput() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.output = nil
	e.outputBytes = 0
	e.dropped = e.nextOutput
	e.truncated = e.truncated || e.nextOutput > 0
}
