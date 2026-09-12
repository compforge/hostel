package amenity

import "github.com/qiankunli/hostel/internal/bed"

// Status is an Amenity-owned global report. Concrete facilities define their
// own fields; the manager only uses lifecycleState for capability projection.
// Component Status remains a scope convention, not a cross-domain base type.
type Status interface{ lifecycleState() string }

// TenantStatus is an Amenity-owned immutable observation. The sealed interface
// permits domain-specific reports without a map of untyped business fields.
type TenantStatus interface{ tenantStatus() }

type BindingStatus struct {
	TenantID TenantID     `json:"tenant_id"`
	Status   TenantStatus `json:"status"`
}

// Status aggregates facility-wide Amenity Status for the Hostel instance,
// alongside the Bed Manager's domain Component Status.
func (m *Manager) Status() map[string]Status {
	out := make(map[string]Status)
	for _, a := range m.List() {
		out[a.Name()] = a.Status()
	}
	return out
}

// BedStatus joins the local Bed identity to live Tenant observations. It does
// not acquire tenants, touch Bed activity or perform remote I/O.
func (m *Manager) BedStatus(id bed.ID) map[string]BindingStatus {
	out := make(map[string]BindingStatus)
	b := m.binding(id)
	if b == nil {
		return out
	}
	b.mu.Lock()
	tenants := make(map[string]Tenant, len(b.tenants))
	for name, t := range b.tenants {
		tenants[name] = t
	}
	b.mu.Unlock()
	for name, t := range tenants {
		out[name] = BindingStatus{t.ID(), t.Status()}
	}
	return out
}

// States projects facility lifecycle for the lightweight capability endpoint.
func (m *Manager) States() map[string]string {
	out := make(map[string]string)
	for name, report := range m.Status() {
		out[name] = report.lifecycleState()
	}
	return out
}
