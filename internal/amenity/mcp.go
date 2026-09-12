package amenity

import (
	"context"
	"errors"
	"sync"

	"github.com/qiankunli/go-stdx/randx"
	"github.com/qiankunli/hostel/pkg/mcpproxy"
	"golang.org/x/sync/semaphore"
)

type MCP struct {
	mu      sync.Mutex
	options mcpproxy.Options
	tenants map[TenantID]*MCPTenant
	closed  bool
}

type MCPTenant struct {
	closeMu *semaphore.Weighted
	id      TenantID
	owner   *MCP
	proxy   *mcpproxy.Proxy
	closed  bool // protected by owner.mu
}

func NewMCP(options mcpproxy.Options) *MCP {
	return &MCP{options: options, tenants: make(map[TenantID]*MCPTenant)}
}
func (a *MCP) Name() string                    { return "mcp" }
func (a *MCP) Start(ctx context.Context) error { return ctx.Err() }
func (a *MCP) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	state := StateIdle
	if len(a.tenants) > 0 {
		state = StateRunning
	}
	if a.closed {
		state = StateClosed
		if len(a.tenants) > 0 {
			state = StateClosing
		}
	}
	return MCPStatus{State: state, Tenants: len(a.tenants)}
}
func (a *MCP) NewTenant(ctx context.Context) (Tenant, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.closed {
		return nil, errors.New("mcp: facility closed")
	}
	t := &MCPTenant{id: TenantID(randx.Hex(16)), owner: a, closeMu: semaphore.NewWeighted(1), proxy: mcpproxy.New(a.options)}
	a.tenants[t.id] = t
	return t, nil
}
func (t *MCPTenant) ID() TenantID           { return t.id }
func (t *MCPTenant) Proxy() *mcpproxy.Proxy { return t.proxy }
func (t *MCPTenant) Status() TenantStatus   { return MCPTenantStatus{t.proxy.Status()} }
func (t *MCPTenant) Close(ctx context.Context) error {
	if err := t.closeMu.Acquire(ctx, 1); err != nil {
		return err
	}
	defer t.closeMu.Release(1)
	t.owner.mu.Lock()
	closed := t.closed
	t.owner.mu.Unlock()
	if closed {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := t.proxy.Close(); err != nil {
		return err
	}
	t.owner.mu.Lock()
	t.closed = true
	delete(t.owner.tenants, t.id)
	t.owner.mu.Unlock()
	return nil
}
func (a *MCP) Close(ctx context.Context) error {
	a.mu.Lock()
	a.closed = true
	tenants := make([]*MCPTenant, 0, len(a.tenants))
	for _, t := range a.tenants {
		tenants = append(tenants, t)
	}
	a.mu.Unlock()
	var result error
	for _, t := range tenants {
		result = errors.Join(result, t.Close(ctx))
	}
	return result
}

// MCPStatus describes the facility's tenant pools, not remote reachability.
type MCPStatus struct {
	State   string `json:"state"`
	Tenants int    `json:"tenants"`
}

func (s MCPStatus) lifecycleState() string { return s.State }

// MCPTenantStatus reports this tenant's proxy lifecycle. Configuration and
// connection facts are supplied by the proxy's own reporting API.
type MCPTenantStatus struct{ mcpproxy.Status }

func (MCPTenantStatus) tenantStatus() {}
