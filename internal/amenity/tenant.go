package amenity

import "context"

// TenantID identifies one tenant within its facility, independently of Bed
// names, local Bed IDs and replaceable resource IDs such as BrowserContext ID.
type TenantID string

func (id TenantID) String() string { return string(id) }

// Tenant is Hostel's facility-use unit, primarily allocated for a Bed. Each
// Amenity owns its concrete resources and defines its domain-specific Status.
type Tenant interface {
	ID() TenantID
	// Close revokes credentials and releases resources. Failures retain cleanup
	// ownership and can be retried; success makes the handle permanently closed.
	Close(context.Context) error
	Status() TenantStatus
}

// TenantFactory is optional. Creating a tenant allocates its identity, not
// necessarily its expensive resources. Actions remain facility-specific.
type TenantFactory interface {
	NewTenant(context.Context) (Tenant, error)
}
