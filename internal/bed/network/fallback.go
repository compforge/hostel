package network

import "github.com/qiankunli/hostel/internal/bed/tool"

func (c Config) WithoutOptionalNamespace(reason string) (Config, bool) {
	if c.Level == Shared || c.NetNS.Effective() != tool.Auto {
		return c, false
	}
	c.Level, c.FallbackReason = Shared, "namespace combination unavailable: "+reason
	return c, true
}
