package view

import (
	"time"

	"github.com/qiankunli/hostel/internal/bed/executor"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
)

type StreamEventType string

const (
	EventExecutionStart StreamEventType = "execution_start"
	EventStdout         StreamEventType = "stdout"
	EventStderr         StreamEventType = "stderr"
	EventExecutionEnd   StreamEventType = "execution_end"
	EventPing           StreamEventType = "ping"
)

type ProcessOutcome struct {
	Kind       executor.ProcessOutcomeKind `json:"kind"`
	ExitCode   *int                        `json:"exit_code,omitempty"`
	Signal     *int                        `json:"signal,omitempty"`
	CoreDumped bool                        `json:"core_dumped,omitempty"`
	Error      string                      `json:"error,omitempty"`
}

type ExecutionResult struct {
	ExecutionID     string               `json:"execution_id"`
	BedID           string               `json:"bed_id"`
	Mode            bed.ExecutionMode    `json:"mode"`
	ExecutorID      string               `json:"executor_id"`
	ExecutorBackend string               `json:"executor_backend"`
	StartedAt       time.Time            `json:"started_at"`
	FinishedAt      time.Time            `json:"finished_at"`
	DurationMs      int64                `json:"duration_ms"`
	Process         ProcessOutcome       `json:"process"`
	Cause           bed.TerminationCause `json:"termination_cause"`
}

type ExecutionOutput struct {
	Sequence int64            `json:"sequence"`
	Stream   bed.OutputStream `json:"stream"`
	Text     string           `json:"text"`
}

type StreamEvent struct {
	Type        StreamEventType  `json:"type"`
	ExecutionID string           `json:"execution_id,omitempty"`
	Sequence    *int64           `json:"sequence,omitempty"`
	Text        string           `json:"text,omitempty"`
	Result      *ExecutionResult `json:"result,omitempty"`
	Timestamp   int64            `json:"timestamp"`
}

func ExecutionOutputFrom(output bed.ExecutionOutput) ExecutionOutput {
	return ExecutionOutput{
		Sequence: output.Sequence,
		Stream:   output.Stream,
		Text:     output.Text,
	}
}

func ExecutionResultFrom(result bed.ExecutionResult) ExecutionResult {
	process := ProcessOutcome{
		Kind:       result.Process.Kind,
		CoreDumped: result.Process.CoreDumped,
		Error:      result.Process.Error,
	}
	switch result.Process.Kind {
	case executor.ProcessExited:
		process.ExitCode = &result.Process.ExitCode
	case executor.ProcessSignaled:
		process.Signal = &result.Process.Signal
	}
	return ExecutionResult{
		ExecutionID:     result.ExecutionID,
		BedID:           result.BedID,
		Mode:            result.Mode,
		ExecutorID:      result.ExecutorID,
		ExecutorBackend: result.ExecutorBackend,
		StartedAt:       result.StartedAt,
		FinishedAt:      result.FinishedAt,
		DurationMs:      result.Duration.Milliseconds(),
		Process:         process,
		Cause:           result.Cause,
	}
}
