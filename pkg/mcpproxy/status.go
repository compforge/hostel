package mcpproxy

// Status is an in-memory pool observation; it does not test remote services.
type Status struct {
	Closed            bool `json:"closed"`
	ConfiguredServers int  `json:"configured_servers"`
	ConnectionEntries int  `json:"connection_entries"`
	ActiveCalls       int  `json:"active_calls"`
}

func (p *Proxy) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := Status{Closed: p.ctx.Err() != nil, ConfiguredServers: len(p.current.servers), ConnectionEntries: len(p.connections)}
	for _, c := range p.connections {
		s.ActiveCalls += c.refs
	}
	return s
}
