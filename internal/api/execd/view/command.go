package view

import (
	"time"

	bed "github.com/qiankunli/hostel/internal/bed/manager"
)

type CommandStatus struct {
	ID         string     `json:"id"`
	Content    string     `json:"content"`
	Running    bool       `json:"running"`
	ExitCode   *int       `json:"exit_code"`
	Error      string     `json:"error"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

func Command(status bed.ExecutionStatus, content string, outputErr error) CommandStatus {
	out := CommandStatus{ID: status.ID, Content: content, Running: status.Running, StartedAt: status.StartedAt, FinishedAt: status.FinishedAt}
	if status.Result != nil {
		n := ExitCode(*status.Result)
		out.ExitCode = &n
		if n != 0 {
			out.Error = string(status.Result.Cause)
		}
	}
	if outputErr != nil {
		out.Error = "command output capture failed"
	}
	return out
}
