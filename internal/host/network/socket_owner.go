package network

import (
	"net/netip"

	"github.com/qiankunli/hostel/protocol"
)

type ListenerState = protocol.ListenerState

const (
	ListenerOwned       = protocol.ListenerOwned
	ListenerForeign     = protocol.ListenerForeign
	ListenerAbsent      = protocol.ListenerAbsent
	ListenerUnavailable = protocol.ListenerUnavailable
)

// ListenerInspection is evidence about one address, not a readiness verdict.
// Inability to inspect must never be interpreted as evidence of a foreign owner.
type ListenerInspection = protocol.ListenerInspection

func listenerMatches(local, target netip.AddrPort) bool {
	l, t := local.Addr().Unmap(), target.Addr().Unmap()
	return local.Port() == target.Port() && (l == t || l.IsUnspecified() && (l.Is4() == t.Is4() || l.Is6()))
}
