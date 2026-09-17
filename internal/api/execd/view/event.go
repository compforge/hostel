package view

import (
	"errors"
	"strconv"

	hostel "github.com/qiankunli/hostel/internal"
	"github.com/qiankunli/hostel/internal/bed/executor"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
)

type StreamEvent struct {
	Type          string       `json:"type"`
	Text          string       `json:"text,omitempty"`
	Timestamp     int64        `json:"timestamp"`
	ExecutionTime int64        `json:"execution_time,omitempty"`
	Error         *StreamError `json:"error,omitempty"`
}
type StreamError struct {
	Name      string   `json:"ename"`
	Value     string   `json:"evalue"`
	Traceback []string `json:"traceback"`
}

func ExitCode(result bed.ExecutionResult) int {
	if result.Cause == bed.CauseTimeout {
		return 124
	}
	code := 125
	if result.Process == nil {
		return code
	}
	switch result.Process.Kind {
	case executor.ProcessExited:
		code = result.Process.ExitCode
	case executor.ProcessSignaled:
		code = 128 + result.Process.Signal
	}
	if code == 0 && result.Cause != bed.CauseNatural {
		return 125
	}
	return code
}
func Terminal(result bed.ExecutionResult) StreamEvent {
	code := ExitCode(result)
	if code == 0 {
		return StreamEvent{Type: "execution_complete", ExecutionTime: result.Duration.Milliseconds()}
	}
	message := ""
	if result.Process != nil {
		message = result.Process.Error
	}
	var preparation *hostel.Error
	if errors.As(result.Err, &preparation) {
		message = preparation.Message()
	}
	return StreamEvent{Type: "error", Error: &StreamError{Name: "CommandExecError", Value: strconv.Itoa(code), Traceback: []string{string(result.Cause), message}}}
}
