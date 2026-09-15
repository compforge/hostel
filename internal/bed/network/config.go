package network

import (
	"fmt"

	"github.com/qiankunli/hostel/internal/bed/tool"
)

type Config struct {
	NetNS          tool.Policy
	Level          Level
	FallbackReason string
}
type Options struct{ NetNS *tool.Policy }

func (c Config) Validate() error {
	if c.Level != "" && c.Level != Shared && c.Level != Private {
		return fmt.Errorf("invalid network level %q", c.Level)
	}
	if c.Level == Shared && c.NetNS == tool.Required {
		return fmt.Errorf("network.netns required conflicts with shared network expectation")
	}
	return c.NetNS.Validate()
}
