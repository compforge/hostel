package executor

import "github.com/qiankunli/hostel/internal/bed/tool"

type Config struct {
	SupervisorInlineLimitBytes int
	Backend                    string
	PIDNS                      tool.Policy
}
type Options struct {
	SupervisorInlineLimitBytes *int
	Backend                    *string
	PIDNS                      *tool.Policy
}
