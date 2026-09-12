package amenity

import (
	"context"
	"fmt"
	"net"

	"github.com/qiankunli/go-stdx/randx"
	"golang.org/x/sync/semaphore"
)

// Browser is one tenant's browser capability. Contexts may be recycled while
// this identity and its CDP credential remain stable.
type Browser interface {
	Tenant
	CDPToken() (string, error)
	// ServeCDP authenticates this tenant's token and closes conn on return.
	// The caller holds a revocable Bed session for the entire proxy lifetime.
	ServeCDP(ctx context.Context, conn net.Conn, workspace, token string, onActivity func()) error
	// CloseContext recycles resources while preserving identity and credentials.
	CloseContext(ctx context.Context) error
	Goto(ctx context.Context, workspace, url string) (title, location string, err error)
	Screenshot(ctx context.Context, workspace, path string) (string, error)
	Text(ctx context.Context, workspace string) (string, error)
	Click(ctx context.Context, workspace, selector string) error
	Type(ctx context.Context, workspace, selector, text string, clear bool) error
	Press(ctx context.Context, workspace, key string) error
	Scroll(ctx context.Context, workspace string, dx, dy int) error
	Wait(ctx context.Context, workspace, selector string) error
}

type browserTenant struct {
	operation *semaphore.Weighted
	id        TenantID
	owner     *chromium
	closing   bool
	closed    bool // protected by owner.mu
}

func (c *chromium) NewTenant(ctx context.Context) (Tenant, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.closed || c.state == StateUnavailable {
		return nil, fmt.Errorf("chromium unavailable: %s", c.reason)
	}
	t := &browserTenant{id: TenantID(randx.Hex(16)), owner: c, operation: semaphore.NewWeighted(1)}
	c.tenantHandles[t.id] = t
	return t, nil
}
func (t *browserTenant) ID() TenantID { return t.id }
func (t *browserTenant) Status() TenantStatus {
	c := t.owner
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.closed {
		return BrowserTenantStatus{Closed: true}
	}
	if t.closing {
		return BrowserTenantStatus{CleanupPending: true}
	}
	if resource := c.contexts[t.id.String()]; resource != nil {
		return BrowserTenantStatus{BrowserReady: resource.tabCtx != nil, CleanupPending: resource.tabCtx == nil}
	}
	return BrowserTenantStatus{}
}
func (t *browserTenant) Close(ctx context.Context) error {
	c := t.owner
	c.mu.Lock()
	if t.closed {
		c.mu.Unlock()
		return nil
	}
	t.closing = true
	delete(c.cdpSecrets, t.id.String())
	c.mu.Unlock()
	if err := t.operation.Acquire(ctx, 1); err != nil {
		return err
	}
	defer t.operation.Release(1)
	if err := c.runtime.Acquire(ctx, 1); err != nil {
		return err
	}
	defer c.runtime.Release(1)
	if err := c.releaseContext(ctx, t.id.String()); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.tenantHandles, t.id)
	t.closed = true
	c.mu.Unlock()
	return nil
}
func (t *browserTenant) CloseContext(ctx context.Context) error {
	c := t.owner
	if err := t.operation.Acquire(ctx, 1); err != nil {
		return err
	}
	defer t.operation.Release(1)
	if err := c.runtime.Acquire(ctx, 1); err != nil {
		return err
	}
	defer c.runtime.Release(1)
	c.mu.Lock()
	closed := t.closed || t.closing || c.closed
	c.mu.Unlock()
	if closed {
		return fmt.Errorf("chromium: tenant %s closed", t.id)
	}
	return c.releaseContext(ctx, t.id.String())
}
func (t *browserTenant) CDPToken() (string, error) {
	return t.owner.CDPToken(t.id.String())
}
func (t *browserTenant) ServeCDP(ctx context.Context, conn net.Conn, workspace, token string, activity func()) error {
	return t.owner.ServeCDP(ctx, conn, t.id.String(), workspace, token, activity)
}
func (t *browserTenant) Goto(ctx context.Context, workspace, url string) (string, string, error) {
	return t.owner.Goto(ctx, t.id.String(), workspace, url)
}
func (t *browserTenant) Screenshot(ctx context.Context, workspace, path string) (string, error) {
	return t.owner.Screenshot(ctx, t.id.String(), workspace, path)
}
func (t *browserTenant) Text(ctx context.Context, workspace string) (string, error) {
	return t.owner.Text(ctx, t.id.String(), workspace)
}
func (t *browserTenant) Click(ctx context.Context, workspace, selector string) error {
	return t.owner.Click(ctx, t.id.String(), workspace, selector)
}
func (t *browserTenant) Type(ctx context.Context, workspace, selector, text string, clear bool) error {
	return t.owner.Type(ctx, t.id.String(), workspace, selector, text, clear)
}
func (t *browserTenant) Press(ctx context.Context, workspace, key string) error {
	return t.owner.Press(ctx, t.id.String(), workspace, key)
}
func (t *browserTenant) Scroll(ctx context.Context, workspace string, dx, dy int) error {
	return t.owner.Scroll(ctx, t.id.String(), workspace, dx, dy)
}
func (t *browserTenant) Wait(ctx context.Context, workspace, selector string) error {
	return t.owner.Wait(ctx, t.id.String(), workspace, selector)
}

var _ Browser = (*browserTenant)(nil)

// BrowserTenantStatus describes the tenant's browser capability. The underlying
// BrowserContext/target identities stay private to Chromium.
type BrowserTenantStatus struct {
	BrowserReady   bool `json:"browser_ready"`
	CleanupPending bool `json:"cleanup_pending"`
	Closed         bool `json:"closed"`
}

func (BrowserTenantStatus) tenantStatus() {}
