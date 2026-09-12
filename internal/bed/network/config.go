package network

import "github.com/qiankunli/hostel/internal/feature"

type Config struct{ NetNS feature.Policy }
type Options struct{ NetNS *feature.Policy }

func (c Config) Validate() error { return c.NetNS.Validate() }
