package network

import "github.com/qiankunli/hostel/internal/bed/tool"

// Selection retains the failure that led to a weaker executable network.
// Err is internal evidence for a later required-capability error, not a failure
// returned to a caller whose isolation floor is already satisfied.
type Selection struct {
	Config
	Err error
}

// Candidates returns permitted network levels in strongest-first order. The
// Component owns these choices; the Bed isolation floor is checked after the
// executable combination is known, not used to weaken the candidate list.
func (c Config) Candidates() []Config {
	c.Level = Private
	if c.NetNS.Effective() == tool.Off {
		c.Level = Shared
		return []Config{c}
	}
	if c.NetNS.Effective() == tool.Required {
		return []Config{c}
	}
	shared := c
	shared.Level = Shared
	return []Config{c, shared}
}
