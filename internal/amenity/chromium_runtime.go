package amenity

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// Runtime changes are exclusive; tenant operations hold one shared permit.
// Status and credentials only take mu, never this gate. Tenant gates serialize
// their own resource operations without blocking another tenant's browser I/O.
const runtimeWeight int64 = 1 << 30

func linkedContext(caller, lifetime context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(lifetime)
	stop := context.AfterFunc(caller, cancel)
	if caller.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}

// initializeBrowserContext attaches on the long-lived context, not a temporary action context.
// Only failure/cancellation tears down the resource; success removes the deadline.
func initializeBrowserContext(caller, lifetime context.Context, abort context.CancelFunc) error {
	done := make(chan struct{})
	stop := context.AfterFunc(caller, func() { abort(); close(done) })
	if caller.Err() != nil {
		abort()
	}
	err := chromedp.Run(lifetime)
	if !stop() {
		<-done
	}
	if caller.Err() != nil {
		return caller.Err()
	}
	return err
}

// enterRuntime leaves one shared permit held on success. The first caller
// starts the browser; concurrent callers join that attempt through the gate.
func (c *chromium) enterRuntime(ctx context.Context) error {
	if err := c.runtime.Acquire(ctx, 1); err != nil {
		return err
	}
	c.mu.Lock()
	running, closed := c.state == StateRunning, c.closed
	c.mu.Unlock()
	if running && !closed {
		return nil
	}
	c.runtime.Release(1)
	if err := c.runtime.Acquire(ctx, runtimeWeight); err != nil {
		return err
	}
	if err := c.startBrowser(ctx); err != nil {
		c.runtime.Release(runtimeWeight)
		return err
	}
	c.runtime.Release(runtimeWeight - 1)
	return nil
}

// startBrowser requires the exclusive runtime gate, but never holds mu over I/O.
func (c *chromium) startBrowser(ctx context.Context) error {
	c.mu.Lock()
	if c.closed || c.state == StateUnavailable {
		reason := c.reason
		c.mu.Unlock()
		return fmt.Errorf("chromium unavailable: %s", reason)
	}
	if c.state == StateRunning {
		c.mu.Unlock()
		return nil
	}
	if wait := time.Until(c.notBefore); wait > 0 {
		c.mu.Unlock()
		return fmt.Errorf("amenity: chromium restart gated for %s", wait.Round(time.Millisecond))
	}
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, c.cfg.ActionTimeout)
	defer cancel()
	base, abort := context.WithCancel(context.Background())
	var allocCtx context.Context
	var allocStop context.CancelFunc
	if c.attach {
		allocCtx, allocStop = chromedp.NewRemoteAllocator(base, c.cfg.CDPURL)
	} else {
		opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
		opts = append(opts, chromedp.ExecPath(c.cfg.ExecPath), chromedp.NoSandbox)
		if c.cfg.DebugPort > 0 {
			opts = append(opts, chromedp.Flag("remote-debugging-port", strconv.Itoa(c.cfg.DebugPort)))
		}
		allocCtx, allocStop = chromedp.NewExecAllocator(base, opts...)
	}
	master, masterCtl := chromedp.NewContext(allocCtx)
	if err := initializeBrowserContext(ctx, master, abort); err != nil {
		abort()
		masterCtl()
		allocStop()
		return fmt.Errorf("amenity: chromium start: %w", err)
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		abort()
		masterCtl()
		allocStop()
		return errors.New("chromium: facility closed")
	}
	c.allocCtx, c.allocStop, c.master, c.masterCtl = allocCtx, func() { abort(); allocStop() }, master, masterCtl
	c.state, c.reason = StateRunning, ""
	c.workers.Add(1)
	c.mu.Unlock()
	go func() { defer c.workers.Done(); <-master.Done(); c.onMasterGone(master) }()
	return nil
}

func (c *chromium) onMasterGone(master context.Context) {
	_ = c.runtime.Acquire(context.Background(), runtimeWeight)
	defer c.runtime.Release(runtimeWeight)
	c.mu.Lock()
	if c.master != master || c.state != StateRunning {
		c.mu.Unlock()
		return
	}
	now := time.Now()
	if now.Sub(c.lastCrash) > 5*time.Minute {
		c.crashCount = 0
	}
	c.crashCount++
	c.lastCrash = now
	backoff := time.Duration(1<<min(c.crashCount-1, 6)) * time.Second
	c.notBefore = now.Add(backoff)
	count, dropped := c.crashCount, len(c.contexts)
	c.mu.Unlock()
	c.stopBrowser()
	log.Printf("amenity: chromium died (crash #%d, %d contexts dropped); restart gated for %s", count, dropped, backoff)
}

// stopBrowser requires the exclusive runtime gate. Detach state before cancelling
// resources so callbacks and status readers never wait for process teardown.
func (c *chromium) stopBrowser() {
	c.mu.Lock()
	contexts := c.contexts
	c.contexts = make(map[string]*browserContext)
	masterCtl, allocStop := c.masterCtl, c.allocStop
	c.masterCtl, c.allocStop, c.master, c.allocCtx = nil, nil, nil, nil
	c.state = StateIdle
	c.mu.Unlock()
	for _, t := range contexts {
		t.tabStop()
	}
	if masterCtl != nil {
		masterCtl()
	}
	if allocStop != nil {
		allocStop()
	}
}

func (c *chromium) acquireTenant(ctx context.Context, id, workspace string) (*browserContext, func(), error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.ActionTimeout)
	c.mu.Lock()
	t := c.tenantHandles[TenantID(id)]
	c.mu.Unlock()
	if t == nil {
		cancel()
		return nil, nil, fmt.Errorf("chromium: tenant %s closed", id)
	}
	if err := t.operation.Acquire(ctx, 1); err != nil {
		cancel()
		return nil, nil, err
	}
	if err := c.enterRuntime(ctx); err != nil {
		t.operation.Release(1)
		cancel()
		return nil, nil, err
	}
	release := func() { c.runtime.Release(1); t.operation.Release(1); cancel() }
	c.mu.Lock()
	closed := t.closing || t.closed
	c.mu.Unlock()
	if closed {
		release()
		return nil, nil, fmt.Errorf("chromium: tenant %s closed", id)
	}
	resource, err := c.prepareContext(ctx, id, workspace)
	if err != nil {
		release()
		return nil, nil, err
	}
	return resource, release, nil
}

// prepareContext holds the tenant gate and a shared runtime permit. Remote
// ownership is recorded immediately, including partially constructed contexts.
func (c *chromium) prepareContext(ctx context.Context, id, workspace string) (*browserContext, error) {
	c.mu.Lock()
	old := c.contexts[id]
	master := c.master
	if c.idleTimer != nil {
		c.stopIdleTimerLocked()
	}
	c.mu.Unlock()
	if old != nil {
		if old.tabCtx != nil {
			return old, nil
		}
		if err := c.releaseContext(ctx, id); err != nil {
			return nil, err
		}
	}
	op, stop := linkedContext(ctx, master)
	defer stop()
	var resource *browserContext
	var targetID target.ID
	err := chromedp.Run(op, chromedp.ActionFunc(func(ctx context.Context) error {
		bctx := cdp.WithExecutor(ctx, chromedp.FromContext(ctx).Browser)
		// TODO(network): configure a tenant proxy when available; BrowserContext
		// does not inherit Bed command netns. See docs/network.md.
		contextID, err := target.CreateBrowserContext().Do(bctx)
		if err != nil {
			return err
		}
		resource = &browserContext{tenantID: id, contextID: contextID, tabStop: func() {}}
		c.mu.Lock()
		c.contexts[id] = resource
		c.mu.Unlock()
		if err := browser.SetDownloadBehavior(browser.SetDownloadBehaviorBehaviorAllow).WithDownloadPath(filepath.Join(workspace, "downloads")).WithBrowserContextID(contextID).Do(bctx); err != nil {
			return err
		}
		targetID, err = target.CreateTarget("about:blank").WithBrowserContextID(contextID).WithNewWindow(true).Do(bctx)
		return err
	}))
	if err != nil {
		return nil, fmt.Errorf("amenity: create browser context for tenant %s: %w", id, err)
	}
	base, abort := context.WithCancel(master)
	tabCtx, tabStop := chromedp.NewContext(base, chromedp.WithTargetID(targetID))
	if err := initializeBrowserContext(ctx, tabCtx, abort); err != nil {
		abort()
		tabStop()
		return nil, fmt.Errorf("amenity: attach tab for tenant %s: %w", id, err)
	}
	ready := &browserContext{tenantID: id, contextID: resource.contextID, tabCtx: tabCtx, tabStop: func() { abort(); tabStop() }}
	c.mu.Lock()
	c.contexts[id] = ready
	c.mu.Unlock()
	return ready, nil
}

// releaseContext holds either the exclusive runtime gate or a tenant gate plus
// a shared runtime permit. A failed disposal retains ownership for retry.
func (c *chromium) releaseContext(parent context.Context, id string) error {
	c.mu.Lock()
	t := c.contexts[id]
	master, running := c.master, c.state == StateRunning
	c.mu.Unlock()
	if t == nil {
		return nil
	}
	if running {
		bounded, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		ctx, stop := linkedContext(bounded, master)
		defer stop()
		if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			bctx := cdp.WithExecutor(ctx, chromedp.FromContext(ctx).Browser)
			if err := target.DisposeBrowserContext(t.contextID).Do(bctx); err != nil {
				contexts, _, lookupErr := target.GetBrowserContexts().Do(bctx)
				if lookupErr != nil {
					return err
				}
				for _, other := range contexts {
					if other == t.contextID {
						return err
					}
				}
			}
			return nil
		})); err != nil {
			return fmt.Errorf("release browser context: %w", err)
		}
	}
	c.mu.Lock()
	delete(c.contexts, id)
	c.scheduleIdleLocked()
	c.mu.Unlock()
	t.tabStop()
	return nil
}

func (c *chromium) scheduleIdleLocked() {
	if len(c.contexts) != 0 || c.attach || c.closed || c.cfg.IdleStop <= 0 || c.state != StateRunning {
		return
	}
	c.stopIdleTimerLocked()
	c.workers.Add(1)
	c.idleTimer = time.AfterFunc(c.cfg.IdleStop, func() {
		defer c.workers.Done()
		_ = c.runtime.Acquire(context.Background(), runtimeWeight)
		defer c.runtime.Release(runtimeWeight)
		c.mu.Lock()
		stop := len(c.contexts) == 0 && c.state == StateRunning && !c.closed
		c.mu.Unlock()
		if stop {
			log.Printf("amenity: chromium idle, stopping")
			c.stopBrowser()
		}
	})
}
func (c *chromium) stopIdleTimerLocked() {
	if c.idleTimer != nil {
		if c.idleTimer.Stop() {
			c.workers.Done()
		}
		c.idleTimer = nil
	}
}

// Close fences new work immediately, then releases only owned contexts. Attach
// mode never stops the external browser; failed disposal remains retryable.
func (c *chromium) Close(ctx context.Context) error {
	c.cancelWork()
	c.mu.Lock()
	c.closed = true
	clear(c.cdpSecrets)
	c.stopIdleTimerLocked()
	c.mu.Unlock()
	if err := c.runtime.Acquire(ctx, runtimeWeight); err != nil {
		return err
	}
	c.mu.Lock()
	ids := make([]string, 0, len(c.contexts))
	for id := range c.contexts {
		ids = append(ids, id)
	}
	c.mu.Unlock()
	var result error
	for _, id := range ids {
		result = errors.Join(result, c.releaseContext(ctx, id))
	}
	if result != nil {
		c.runtime.Release(runtimeWeight)
		return result
	}
	c.stopBrowser()
	c.mu.Lock()
	for id, t := range c.tenantHandles {
		t.closed = true
		delete(c.tenantHandles, id)
	}
	c.state = StateClosed
	c.mu.Unlock()
	c.runtime.Release(runtimeWeight)
	done := make(chan struct{})
	go func() { c.workers.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
