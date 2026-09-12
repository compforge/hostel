// Package amenity manages independent facilities and their tenants.
// Bed bindings belong to Manager; facilities do not interpret Bed identities.
package amenity

import "context"

const (
	StateUnavailable = "unavailable"
	StateIdle        = "idle"
	StateRunning     = "running"
	StateClosed      = "closed"
	StateClosing     = "closing"
)

// Amenity is one independent facility. Start initializes without requiring
// eager resource allocation. Close joins all work owned by the facility.
// +spec=`Expensive resources may start on demand; Status never waits for resource I/O.`
type Amenity interface {
	Name() string
	Start(context.Context) error
	Close(context.Context) error
	// Status returns facility-wide Component Status without probes or remote I/O.
	Status() Status
}
