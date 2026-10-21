package protocol

// PortMappingStatus is the source of allocated Bed and Host ports.
// Callers derive addresses from these facts and their reachable carrier.
type PortMappingStatus struct {
	Name        string `json:"name"`
	Protocol    string `json:"protocol"`
	Pillow      string `json:"pillow"`
	ExecutionID string `json:"execution_id,omitempty"`
	Network     string `json:"network"`
	State       string `json:"state"`
	BedPort     int    `json:"bed_port"`
	HostPort    int    `json:"host_port,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type ListenerState string

// ListenerInspection is evidence about an address, not a readiness verdict.
type ListenerInspection struct {
	State  ListenerState `json:"state"`
	Method string        `json:"method"`
	Reason string        `json:"reason,omitempty"`
}

const (
	ListenerOwned       ListenerState = "owned"
	ListenerForeign     ListenerState = "foreign"
	ListenerAbsent      ListenerState = "not_listening"
	ListenerUnavailable ListenerState = "unavailable"
)
