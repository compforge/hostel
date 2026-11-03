package network

import "github.com/qiankunli/hostel/internal/bed/tool"

type Config struct{ NetNS tool.Policy }
type Options struct{ NetNS *tool.Policy }

func (c Config) Validate() error { return c.NetNS.Validate() }
