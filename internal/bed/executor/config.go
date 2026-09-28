package executor

import "github.com/qiankunli/hostel/internal/bed/tool"

type Config struct {
	Backend   string
	PIDNS     tool.Policy
	selection *tool.Status
}
type Options struct {
	Backend *string
	PIDNS   *tool.Policy
}

// WithPIDNSSelection freezes the tested startup combination without losing the
// requested policy or its probe evidence. Live factories may not reselect it.
func (c Config) WithPIDNSSelection(s tool.Status) Config {
	c.selection = &s
	return c
}

// WithoutOptionalPIDNS retains policy/probe evidence while excluding a failed
// composition. It is used only by the startup combination selector.
func (c Config) WithoutOptionalPIDNS(s tool.Status, reason string) (Config, bool) {
	if c.PIDNS.Effective() != tool.Auto || !s.Selected {
		return c, false
	}
	s.Selected, s.Reason = false, reason
	return c.WithPIDNSSelection(s), true
}
