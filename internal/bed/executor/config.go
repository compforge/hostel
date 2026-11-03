package executor

import "github.com/qiankunli/hostel/internal/bed/tool"

type Config struct {
	Backend string
	PIDNS   tool.Policy
}
type Options struct {
	Backend *string
	PIDNS   *tool.Policy
}
