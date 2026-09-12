package amenity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/qiankunli/hostel/internal/bed"
)

// Manager composes independent facilities and owns Bed-to-Tenant bindings.
// Facility registration is fixed before Start. No facility calls back into it
// while holding its resource locks.
type Manager struct {
	mu        sync.RWMutex
	lifecycle sync.Mutex
	amenities []Amenity
	beds      map[bed.ID]*bedBinding
	started   bool
	closed    bool
}

type bedBinding struct {
	mu      sync.Mutex
	closing bool
	tenants map[string]Tenant
}

func NewManager() *Manager { return &Manager{beds: make(map[bed.ID]*bedBinding)} }

func (m *Manager) Register(a Amenity) error {
	if m == nil || a == nil {
		return errors.New("amenity: cannot register nil facility")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started || m.closed {
		return errors.New("amenity: registration is closed")
	}
	for _, existing := range m.amenities {
		if existing.Name() == a.Name() {
			return fmt.Errorf("amenity: duplicate facility %q", a.Name())
		}
	}
	m.amenities = append(m.amenities, a)
	return nil
}
func (m *Manager) List() []Amenity {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Amenity(nil), m.amenities...)
}
func (m *Manager) Find(name string) Amenity {
	for _, a := range m.List() {
		if a.Name() == name {
			return a
		}
	}
	return nil
}

// AdmitBed records an exact local identity before requests can allocate tenants.
func (m *Manager) AdmitBed(id bed.ID) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("amenity: manager closed")
	}
	if m.beds[id] == nil {
		m.beds[id] = &bedBinding{tenants: make(map[string]Tenant)}
	}
	return nil
}
func (m *Manager) binding(id bed.ID) *bedBinding {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.beds[id]
}

// Acquire requires a Bed operation or session lease. The per-Bed lock prevents
// duplicate creation and fences retiring identities without blocking other Beds.
func (m *Manager) Acquire(ctx context.Context, id bed.ID, name string) (Tenant, error) {
	b := m.binding(id)
	if b == nil {
		return nil, fmt.Errorf("amenity: bed %s is not admitted", id)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closing {
		return nil, fmt.Errorf("amenity: bed %s is closing", id)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t := b.tenants[name]; t != nil {
		return t, nil
	}
	a := m.Find(name)
	factory, ok := a.(TenantFactory)
	if !ok {
		return nil, fmt.Errorf("amenity %s: tenants unavailable", name)
	}
	t, err := factory.NewTenant(ctx)
	if err != nil {
		return nil, fmt.Errorf("amenity %s: create tenant: %w", name, err)
	}
	b.tenants[name] = t
	slog.DebugContext(ctx, "amenity tenant bound", "bed_id", id, "amenity", name, "tenant_id", t.ID())
	return t, nil
}

// ReleaseBed attempts every binding. Failed tenants remain owned for retry;
// successful ones are removed and cannot be closed again by a later retry.
func (m *Manager) ReleaseBed(ctx context.Context, id bed.ID) error {
	b := m.binding(id)
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closing = true
	var result error
	for name, t := range b.tenants {
		if err := t.Close(ctx); err != nil {
			result = errors.Join(result, fmt.Errorf("amenity %s tenant %s: %w", name, t.ID(), err))
			slog.WarnContext(ctx, "amenity tenant cleanup pending", "bed_id", id, "amenity", name, "tenant_id", t.ID(), "error", err)
			continue
		}
		delete(b.tenants, name)
	}
	if result == nil {
		m.mu.Lock()
		delete(m.beds, id)
		m.mu.Unlock()
	}
	return result
}

// Browser resolves the admitted Bed binding to a typed tenant capability.
func (m *Manager) Browser(ctx context.Context, id bed.ID) (Browser, error) {
	t, err := m.Acquire(ctx, id, "chromium")
	if err != nil {
		return nil, err
	}
	br, ok := t.(Browser)
	if !ok {
		return nil, errors.New("chromium: tenant has no browser capability")
	}
	return br, nil
}
func (m *Manager) MCP(ctx context.Context, id bed.ID) (*MCPTenant, error) {
	t, err := m.Acquire(ctx, id, "mcp")
	if err != nil {
		return nil, err
	}
	mcp, ok := t.(*MCPTenant)
	if !ok {
		return nil, errors.New("mcp: tenant has no proxy capability")
	}
	return mcp, nil
}

// CDPToken binds a tenant only when the facility can actually expose a proxy.
// The token is tenant-owned; the caller attaches its own Bed routing name.
func (m *Manager) CDPToken(ctx context.Context, id bed.ID) (string, error) {
	c, ok := m.Find("chromium").(*chromium)
	if !ok || !c.proxyable() {
		return "", errors.New("chromium: CDP proxy unavailable")
	}
	br, err := m.Browser(ctx, id)
	if err != nil {
		return "", err
	}
	return br.CDPToken()
}
