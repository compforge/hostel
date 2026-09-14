package network

import "net/netip"

type ListenerState string

const (
	ListenerOwned       ListenerState = "owned"
	ListenerForeign     ListenerState = "foreign"
	ListenerAbsent      ListenerState = "not_listening"
	ListenerUnavailable ListenerState = "unavailable"
)

// ListenerInspection is evidence about one address, not a readiness verdict.
// Inability to inspect must never be interpreted as evidence of a foreign owner.
type ListenerInspection struct {
	State  ListenerState `json:"state"`
	Method string        `json:"method"`
	Reason string        `json:"reason,omitempty"`
}

func listenerMatches(local, target netip.AddrPort) bool {
	l, t := local.Addr().Unmap(), target.Addr().Unmap()
	return local.Port() == target.Port() && (l == t || l.IsUnspecified() && (l.Is4() == t.Is4() || l.Is6()))
}
