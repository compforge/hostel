package network

import "github.com/qiankunli/hostel/internal/bed/tool"

type Config struct{ NetNS, UIDNFT tool.Policy }
type Options struct{ NetNS, UIDNFT *tool.Policy }

func (c Config) Validate() error {
	if err := c.NetNS.Validate(); err != nil {
		return err
	}
	return c.UIDNFT.Validate()
}
