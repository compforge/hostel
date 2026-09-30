package executor

import (
	"maps"

	"github.com/qiankunli/hostel/internal/bed/tool"
)

type Config struct {
	Backend   string
	PIDNS     tool.Policy
	selection map[string]tool.Status
}
type Options struct {
	Backend *string
	PIDNS   *tool.Policy
}

// WithSelection freezes the tested tool choices without losing their policies
// or probe evidence. Live factories must recreate this selection exactly.
func (c Config) WithSelection(tools map[string]tool.Status) Config {
	c.selection = maps.Clone(tools)
	return c
}

// NextCombination owns optional process-domain fallback. New namespace tools
// extend this policy here, without adding mechanism branches to Bed Manager.
func (c Config) NextCombination(tools map[string]tool.Status, reason string) (Config, bool) {
	s := tools["pidns"]
	if c.PIDNS.Effective() != tool.Auto || !s.Selected {
		return c, false
	}
	tools = maps.Clone(tools)
	s.Selected, s.Reason = false, reason
	tools["pidns"] = s
	return c.WithSelection(tools), true
}
