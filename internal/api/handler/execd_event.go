package handler

import (
	apiview "github.com/qiankunli/hostel/internal/api/view"
	"github.com/qiankunli/hostel/internal/bed/executor"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
	"strconv"
)

type execdStreamEvent struct {
	Type          string            `json:"type"`
	Text          string            `json:"text,omitempty"`
	Timestamp     int64             `json:"timestamp"`
	ExecutionTime int64             `json:"execution_time,omitempty"`
	Error         *execdStreamError `json:"error,omitempty"`
}
type execdStreamError struct {
	Name      string   `json:"ename"`
	Value     string   `json:"evalue"`
	Traceback []string `json:"traceback"`
}

// A native execution_end is a typed outcome, not an unconditional success.
// OpenSandbox ends a failed command with error, and a successful one with
// execution_complete. Preserve termination cause in the error traceback.
func execdEvent(ev apiview.StreamEvent) execdStreamEvent {
	out := execdStreamEvent{Type: string(ev.Type), Text: ev.Text, Timestamp: ev.Timestamp}
	switch ev.Type {
	case apiview.EventExecutionStart:
		out.Type, out.Text = "init", ev.ExecutionID
	case apiview.EventExecutionEnd:
		result := ev.Result
		code := 125
		if result != nil {
			switch {
			case result.Cause == bed.CauseTimeout:
				code = 124
			case result.Process.Kind == executor.ProcessExited && result.Process.ExitCode != nil:
				code = *result.Process.ExitCode
			case result.Process.Signal != nil:
				code = 128 + *result.Process.Signal
			}
			if code == 0 && result.Cause != bed.CauseNatural {
				code = 125
			}
		}
		if code == 0 {
			out.Type = "execution_complete"
			out.ExecutionTime = result.DurationMs
		} else {
			out.Type = "error"
			trace := []string{"execution failed"}
			if result != nil {
				trace = []string{string(result.Cause), result.Process.Error}
			}
			out.Error = &execdStreamError{Name: "CommandExecError", Value: strconv.Itoa(code), Traceback: trace}
		}
	}
	return out
}
