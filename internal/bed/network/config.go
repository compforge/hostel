package network

import (
	"fmt"

	"github.com/qiankunli/hostel/internal/bed/tool"
)

type Config struct {
	NetNS, UIDNFT tool.Policy
	// MTU is used only when startup egress MTU discovery fails.
	MTU int
}
type Options struct {
	NetNS, UIDNFT *tool.Policy
	MTU           *int
}

func (c Config) Validate() error {
	if c.MTU < 68 || c.MTU > 65535 {
		return fmt.Errorf("fallback network MTU must be between 68 and 65535 bytes, got %d", c.MTU)
	}
	if err := c.NetNS.Validate(); err != nil {
		return err
	}
	return c.UIDNFT.Validate()
}
