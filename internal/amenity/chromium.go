// Copyright 2026 Li Qiankun
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package amenity

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/qiankunli/go-stdx/randx"
)

// ChromiumConfig selects launch-or-attach (docs/amenity.md §5).
type ChromiumConfig struct {
	// ExecPath launches a hostel-owned Chromium ("" = probe common locations).
	ExecPath string
	// CDPURL attaches to an existing instance (sidecar/supervisor deployments
	// — the execd --jupyter-host shape); hostel then slices but does not own
	// the process.
	CDPURL string
	// IdleStop stops a LAUNCHED browser this long after its last tenant is
	// released (0 = never stop). Frees hundreds of MB between bursts.
	IdleStop time.Duration
	// ActionTimeout bounds one browser action (navigate, shot...).
	ActionTimeout time.Duration
	// DebugPort fixes the launched browser's --remote-debugging-port so the
	// per-bed CDP proxy has a stable upstream (chromedp alone would pick a
	// random port hostel can't dial back). 0 disables the proxy in launch mode;
	// attach mode always uses CDPURL as upstream.
	DebugPort int
}

// chromium is the first amenity: one shared browser, one isolated
// BrowserContext per bed.
type chromium struct {
	cfg    ChromiumConfig
	attach bool

	mu            sync.Mutex
	workers       sync.WaitGroup
	tenantHandles map[TenantID]*browserTenant
	reason        string
	closed        bool
	state         string
	allocCtx      context.Context
	allocStop     context.CancelFunc
	master        context.Context // chromedp browser-level context
	masterCtl     context.CancelFunc
	contexts      map[string]*browserContext
	// Credentials belong to tenant identities, independently of replaceable
	// contexts. Minting an endpoint never launches the browser.
	cdpSecrets map[string]string
	idleTimer  *time.Timer

	// Crash supervision (the supervisor is the amenity itself, in-daemon —
	// docs/kernel.md 〈进程树〉): a watcher on the master context detects the
	// browser dying and flips back to idle with contexts dropped, so the next action
	// lazily rebuilds resources under the same Tenant identity — no restart storm, and a bed
	// simply sees a fresh browser. notBefore gates ensureRunning with
	// exponential backoff so a crash-looping browser can't melt the pod.
	crashCount int
	lastCrash  time.Time
	notBefore  time.Time
}

type browserContext struct {
	tenantID  string
	contextID cdp.BrowserContextID
	tabCtx    context.Context
	tabStop   context.CancelFunc
}

// chromiumCandidates are probed when --chromium-path is unset.
var chromiumCandidates = []string{
	"chromium", "chromium-browser", "google-chrome", "google-chrome-stable",
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"/Applications/Chromium.app/Contents/MacOS/Chromium",
}

// NewChromium assembles configuration; Start owns availability probes.
func NewChromium(cfg ChromiumConfig) *chromium {
	if cfg.ActionTimeout <= 0 {
		cfg.ActionTimeout = 30 * time.Second
	}
	return &chromium{cfg: cfg, state: StateUnavailable, reason: "not_started",
		contexts: make(map[string]*browserContext), tenantHandles: make(map[TenantID]*browserTenant), cdpSecrets: make(map[string]string)}
}

func (c *chromium) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.closed {
		return fmt.Errorf("chromium: facility closed")
	}
	if c.state != StateUnavailable {
		return nil
	}
	if c.cfg.CDPURL != "" {
		c.attach = true
		if _, err := c.upstreamWSURL(ctx); err != nil {
			// Keep the configured facility visible without exposing its endpoint.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.reason = "attach_probe_failed"
			log.Printf("amenity: chromium unavailable: %s", c.reason)
			return nil
		}
	} else {
		path := c.cfg.ExecPath
		if path == "" {
			for _, candidate := range chromiumCandidates {
				if resolved, err := exec.LookPath(candidate); err == nil {
					path = resolved
					break
				}
			}
		} else if resolved, err := exec.LookPath(path); err == nil {
			path = resolved
		} else {
			path = ""
		}
		if path == "" {
			c.reason = "executable_not_found"
			return nil
		}
		c.cfg.ExecPath = path
	}
	c.state, c.reason = StateIdle, ""
	return nil
}

func (c *chromium) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	mode := "launch"
	if c.attach {
		mode = "attach"
	}
	state, reason := c.state, c.reason
	if c.closed && state != StateClosed {
		state, reason = StateClosing, "cleanup_pending"
	}
	return ChromiumStatus{State: state, Reason: reason, Mode: mode, Tenants: len(c.tenantHandles)}
}

func (c *chromium) Name() string { return "chromium" }

func (c *chromium) State() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// ensureRunning starts (or attaches to) the shared browser on first demand.
// Caller holds c.mu.
func (c *chromium) ensureRunning() error {
	if c.closed || c.state == StateUnavailable {
		return fmt.Errorf("chromium unavailable: %s", c.reason)
	}
	if c.state == StateRunning {
		return nil
	}
	// Crash-loop guard: after the watcher recorded a death, restarts are gated.
	// The error is the caller's signal to retry later — deliberately NOT a
	// blocking sleep, which would pin c.mu and freeze every bed's actions.
	if wait := time.Until(c.notBefore); wait > 0 {
		return fmt.Errorf("amenity: chromium restart gated for %s (crash #%d)", wait.Round(time.Millisecond), c.crashCount)
	}
	base := context.Background()
	if c.attach {
		c.allocCtx, c.allocStop = chromedp.NewRemoteAllocator(base, c.cfg.CDPURL)
	} else {
		opts := append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(c.cfg.ExecPath),
			// Chromium's own sandbox is unreliable in containers: mandatory-off as
			// root, and needs userns/setuid otherwise. Reachability of this process
			// from bed code is NOT a uid story anymore (runtime is often one non-root
			// uid, beds keep the host pid ns) — it is governed by the semi-trusted
			// model documented in docs/amenity.md ("诚实边界").
			chromedp.NoSandbox,
		)
		if c.cfg.DebugPort > 0 {
			// Fixed port so the per-bed CDP proxy has a stable upstream to dial;
			// chromedp's own stderr-parsed ws URL is not exposed by its API.
			opts = append(opts, chromedp.Flag("remote-debugging-port", strconv.Itoa(c.cfg.DebugPort)))
		}
		c.allocCtx, c.allocStop = chromedp.NewExecAllocator(base, opts...)
	}
	c.master, c.masterCtl = chromedp.NewContext(c.allocCtx)
	// Force the browser up now so failures surface here, not mid-action.
	if err := chromedp.Run(c.master); err != nil {
		c.stopLocked()
		return fmt.Errorf("amenity: chromium start: %w", err)
	}
	c.state = StateRunning
	c.workers.Add(1)
	go func(master context.Context) { defer c.workers.Done(); c.watchMaster(master) }(c.master)
	return nil
}

// watchMaster turns the master context's death into supervision: chromedp
// cancels it when the browser process exits (crash) — and orderly stops cancel
// it too, which onMasterGone tells apart by state.
func (c *chromium) watchMaster(master context.Context) {
	<-master.Done()
	c.onMasterGone(master)
}

// onMasterGone handles one master-context death. Only an UNEXPECTED death of
// the CURRENT instance counts as a crash: orderly stops (idle-stop timer,
// stopLocked) already flipped state off Running before releasing the lock, and
// a stale watcher's master no longer matches.
func (c *chromium) onMasterGone(master context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.master != master || c.state != StateRunning {
		return
	}
	now := time.Now()
	if now.Sub(c.lastCrash) > 5*time.Minute {
		c.crashCount = 0 // stable for a while: earlier crashes are history
	}
	c.crashCount++
	c.lastCrash = now
	backoff := time.Duration(1<<min(c.crashCount-1, 6)) * time.Second // 1s → 64s cap
	c.notBefore = now.Add(backoff)
	dropped := len(c.contexts)
	c.stopLocked()
	log.Printf("amenity: chromium died (crash #%d, %d tenant(s) dropped); restart gated for %s",
		c.crashCount, dropped, backoff)
}

// stopLocked tears the browser down. Caller holds c.mu.
func (c *chromium) stopLocked() {
	for id, t := range c.contexts {
		t.tabStop()
		delete(c.contexts, id)
	}
	if c.masterCtl != nil {
		c.masterCtl()
		c.masterCtl = nil
	}
	if c.allocStop != nil {
		c.allocStop()
		c.allocStop = nil
	}
	c.state = StateIdle
}

// tenant resolves private browser resources for a Hostel tenant, creating
// context and tab lazily.
// Caller holds c.mu.
func (c *chromium) tenant(tenantID, workspace string) (*browserContext, error) {
	if c.closed || (c.tenantHandles[TenantID(tenantID)] == nil || c.tenantHandles[TenantID(tenantID)].closing) {
		return nil, fmt.Errorf("chromium: tenant %s closed", tenantID)
	}
	if t, ok := c.contexts[tenantID]; ok {
		if t.tabCtx != nil {
			return t, nil
		}
		// A partial allocation still owns a remote context. Dispose it before
		// retrying, preserving ownership if the remote operation fails again.
		if err := c.releaseContextLocked(context.Background(), tenantID); err != nil {
			return nil, err
		}
	}
	if err := c.ensureRunning(); err != nil {
		return nil, err
	}
	if c.idleTimer != nil {
		c.stopIdleTimerLocked()
	}

	var contextID cdp.BrowserContextID
	var targetID target.ID
	err := chromedp.Run(c.master, chromedp.ActionFunc(func(ctx context.Context) error {
		// Target.* context management is a BROWSER-session domain — route via
		// the browser executor, not the page session (else: Not allowed).
		bctx := cdp.WithExecutor(ctx, chromedp.FromContext(ctx).Browser)
		// TODO(network): set this Bed's proxyServer when the Bed-scoped egress
		// proxy is available. BrowserContext does not inherit a command's netns;
		// validate bypass/QUIC/WebRTC paths before advertising browser enforcement.
		// See docs/backlog.md and docs/network.md.
		id, err := target.CreateBrowserContext().Do(bctx)
		if err != nil {
			return err
		}
		contextID = id
		// Publish ownership as soon as the remote context exists; later target or
		// session setup can fail and must not orphan it.
		c.contexts[tenantID] = &browserContext{tenantID: tenantID, contextID: id, tabStop: func() {}}
		// Route downloads into the bed's own workspace.
		_ = browser.SetDownloadBehavior(browser.SetDownloadBehaviorBehaviorAllow).
			WithDownloadPath(filepath.Join(workspace, "downloads")).
			WithBrowserContextID(id).Do(bctx)
		// New headless refuses tab creation in a fresh context without an
		// explicit window ("no browser is open").
		targetID, err = target.CreateTarget("about:blank").WithBrowserContextID(id).WithNewWindow(true).Do(bctx)
		return err
	}))
	if err != nil {
		return nil, fmt.Errorf("amenity: chromium context for tenant %s: %w", tenantID, err)
	}
	tabCtx, tabStop := chromedp.NewContext(c.master, chromedp.WithTargetID(targetID))
	// Attach the target on the LONG-LIVED tab context now. Otherwise the first
	// per-action call would attach on its short-lived timeout context, and the
	// attach would be torn down when that context is cancelled — every action
	// after the first would hang (no session).
	if err := chromedp.Run(tabCtx); err != nil {
		tabStop()
		return nil, fmt.Errorf("amenity: chromium attach tab for tenant %s: %w", tenantID, err)
	}
	t := &browserContext{tenantID: tenantID, contextID: contextID, tabCtx: tabCtx, tabStop: tabStop}
	c.contexts[tenantID] = t
	return t, nil
}

// CDPToken mints or returns a tenant-owned proxy secret.
// Mint-only: no browser resources are allocated; the lazy boot point is the
// first proxy dial (ServeCDP). This is what lets hostel hand every bed its
// endpoint eagerly (spawn env) while keeping the browser demand-started.
func (c *chromium) CDPToken(tenantID string) (string, error) {
	if !c.proxyable() {
		return "", fmt.Errorf("amenity: chromium CDP proxy unavailable (launch mode needs --chromium-debug-port)")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || (c.tenantHandles[TenantID(tenantID)] == nil || c.tenantHandles[TenantID(tenantID)].closing) {
		return "", fmt.Errorf("chromium: tenant %s closed", tenantID)
	}
	if s, ok := c.cdpSecrets[tenantID]; ok {
		return s, nil
	}
	s := randx.Hex(16)
	c.cdpSecrets[tenantID] = s
	return s, nil
}

// proxyable reports whether a stable upstream CDP endpoint exists: attach mode
// always has one (CDPURL), launch mode only with a fixed debug port.
func (c *chromium) proxyable() bool {
	return c.attach || c.cfg.DebugPort > 0
}

// upstreamHTTPBase is the browser's own devtools HTTP endpoint ("/json/version"
// lives there). Valid only when proxyable().
func (c *chromium) upstreamHTTPBase() string {
	if c.attach {
		return strings.TrimSuffix(c.cfg.CDPURL, "/")
	}
	return "http://127.0.0.1:" + strconv.Itoa(c.cfg.DebugPort)
}

// upstreamWSURL resolves the browser-level websocket URL via /json/version.
// Resolved per proxy session, not cached: it changes whenever the shared
// browser restarts (crash supervision, idle-stop).
func (c *chromium) upstreamWSURL(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.upstreamHTTPBase()+"/json/version", nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("amenity: chromium /json/version: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("chromium version endpoint returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", err
	}
	var v struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.Unmarshal(body, &v); err != nil || v.WebSocketDebuggerURL == "" {
		return "", fmt.Errorf("amenity: chromium /json/version: no webSocketDebuggerUrl (err=%v)", err)
	}
	return v.WebSocketDebuggerURL, nil
}

// ServeCDP implements Browser: authenticate the token, then bridge the client
// websocket to the shared browser filtered to this bed's contexts.
func (c *chromium) ServeCDP(ctx context.Context, conn net.Conn, tenantID, workspace, token string, onActivity func()) error {
	defer conn.Close()
	c.mu.Lock()
	secret, ok := c.cdpSecrets[tenantID]
	authorized := ok && token != "" && subtle.ConstantTimeCompare([]byte(secret), []byte(token)) == 1
	if !authorized {
		c.mu.Unlock()
		// The secret is only handed out via hostel (spawn env / browser/info);
		// a mismatch means a guessed bed id or a token that outlived its bed.
		// Refuse — never fall back to unfiltered CDP.
		return fmt.Errorf("amenity: chromium CDP: unauthorized for tenant %s", tenantID)
	}
	// Authorized: ensure the tenant NOW. This is the lazy boot point — the
	// browser starts on the first dial, not when the endpoint was handed out.
	t, err := c.tenant(tenantID, workspace)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	contextID := t.contextID
	c.mu.Unlock()
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	upstream, err := c.upstreamWSURL(dialCtx)
	cancel()
	if err != nil {
		return err
	}
	return proxyCDP(ctx, conn, upstream, tenantID, string(contextID), onActivity)
}

// ReleaseTenant releases only a tenant's current browser resources. The
// identity and credential live until browserTenant.Close.
func (c *chromium) ReleaseTenant(tenantID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.releaseContextLocked(context.Background(), tenantID)
}

func (c *chromium) releaseContextLocked(parent context.Context, tenantID string) error {
	// Deliberately does NOT touch cdpSecrets: browser/close (bed action API)
	// also lands here to recycle the slice, and the bed's env-injected proxy
	// endpoint must keep working afterwards — the next dial just re-ensures a
	// fresh tenant. Secrets die when the tenant is closed.
	t, ok := c.contexts[tenantID]
	if !ok {
		return nil
	}
	if c.state == StateRunning {
		ctx, cancel := context.WithTimeout(c.master, 5*time.Second)
		stop := context.AfterFunc(parent, cancel)
		defer stop()
		defer cancel()
		if err := parent.Err(); err != nil {
			return err
		}
		if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			bctx := cdp.WithExecutor(ctx, chromedp.FromContext(ctx).Browser)
			if err := target.DisposeBrowserContext(t.contextID).Do(bctx); err != nil {
				// A prior dispose may have succeeded while its reply was lost.
				// Confirm absence before forgetting ownership; otherwise retry later.
				contexts, _, lookupErr := target.GetBrowserContexts().Do(bctx)
				if lookupErr != nil {
					return err
				}
				for _, id := range contexts {
					if id == t.contextID {
						return err
					}
				}
			}
			return nil
		})); err != nil {
			return fmt.Errorf("release browser context: %w", err)
		}
	}
	delete(c.contexts, tenantID)
	t.tabStop()
	if len(c.contexts) == 0 && !c.attach && !c.closed && c.cfg.IdleStop > 0 && c.state == StateRunning {
		c.stopIdleTimerLocked()
		c.workers.Add(1)
		c.idleTimer = time.AfterFunc(c.cfg.IdleStop, func() {
			defer c.workers.Done()
			c.mu.Lock()
			defer c.mu.Unlock()
			// Re-check under lock: a tenant may have arrived meanwhile.
			if len(c.contexts) == 0 && c.state == StateRunning && !c.closed {
				log.Printf("amenity: chromium idle for %s, stopping", c.cfg.IdleStop)
				c.stopLocked()
			}
		})
	}
	return nil
}

// run executes actions in the bed's tab with the action timeout applied.
func (c *chromium) run(ctx context.Context, tenantID, workspace string, actions ...chromedp.Action) error {
	c.mu.Lock()
	t, err := c.tenant(tenantID, workspace)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	actx, cancel := context.WithTimeout(t.tabCtx, c.cfg.ActionTimeout)
	defer cancel()
	// Honor the caller's cancellation too (HTTP request context).
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-actx.Done():
		}
	}()
	return chromedp.Run(actx, actions...)
}

func (c *chromium) Goto(ctx context.Context, tenantID, workspace, url string) (string, string, error) {
	var title, loc string
	err := c.run(ctx, tenantID, workspace,
		chromedp.Navigate(url),
		chromedp.Title(&title),
		chromedp.Location(&loc),
	)
	return title, loc, err
}

func (c *chromium) Text(ctx context.Context, tenantID, workspace string) (string, error) {
	var text string
	err := c.run(ctx, tenantID, workspace,
		chromedp.Text("body", &text, chromedp.ByQuery))
	return text, err
}

func (c *chromium) Click(ctx context.Context, tenantID, workspace, selector string) error {
	return c.run(ctx, tenantID, workspace,
		chromedp.WaitVisible(selector, chromedp.ByQuery),
		chromedp.Click(selector, chromedp.ByQuery))
}

func (c *chromium) Type(ctx context.Context, tenantID, workspace, selector, text string, clear bool) error {
	actions := []chromedp.Action{chromedp.WaitVisible(selector, chromedp.ByQuery)}
	if clear {
		// Focus the node, then empty the FOCUSED element — no selector goes
		// into JS (no injection), and it's deterministic where chromedp.Clear
		// / SetValue are flaky in headless. SendKeys after still fires real
		// keyboard events, which SPAs rely on.
		actions = append(actions,
			chromedp.Focus(selector, chromedp.ByQuery),
			chromedp.Evaluate(`document.activeElement && (document.activeElement.value = "")`, nil))
	}
	actions = append(actions, chromedp.SendKeys(selector, text, chromedp.ByQuery))
	return c.run(ctx, tenantID, workspace, actions...)
}

// namedKeys maps friendly key names to their key-event runes; anything not
// listed is sent as a literal (a single char like "a" works as-is).
var namedKeys = map[string]string{
	"Enter": kb.Enter, "Tab": kb.Tab, "Escape": kb.Escape, "Backspace": kb.Backspace,
	"Delete": kb.Delete, "ArrowDown": kb.ArrowDown, "ArrowUp": kb.ArrowUp,
	"ArrowLeft": kb.ArrowLeft, "ArrowRight": kb.ArrowRight, "PageDown": kb.PageDown,
	"PageUp": kb.PageUp, "Home": kb.Home, "End": kb.End,
}

func (c *chromium) Press(ctx context.Context, tenantID, workspace, key string) error {
	send := key
	if mapped, ok := namedKeys[key]; ok {
		send = mapped
	}
	return c.run(ctx, tenantID, workspace, chromedp.KeyEvent(send))
}

func (c *chromium) Scroll(ctx context.Context, tenantID, workspace string, dx, dy int) error {
	// Numeric-only interpolation — no injection surface.
	js := fmt.Sprintf("window.scrollBy(%d, %d)", dx, dy)
	return c.run(ctx, tenantID, workspace, chromedp.Evaluate(js, nil))
}

func (c *chromium) Wait(ctx context.Context, tenantID, workspace, selector string) error {
	return c.run(ctx, tenantID, workspace, chromedp.WaitVisible(selector, chromedp.ByQuery))
}

func (c *chromium) Screenshot(ctx context.Context, tenantID, workspace, relPath string) (string, error) {
	if relPath == "" {
		relPath = fmt.Sprintf("screenshots/shot-%d.png", time.Now().UnixMilli())
	}
	rel := filepath.ToSlash(filepath.Clean(relPath))
	if strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("amenity: screenshot path escapes the workspace: %q", relPath)
	}
	var buf []byte
	if err := c.run(ctx, tenantID, workspace, chromedp.CaptureScreenshot(&buf)); err != nil {
		return "", err
	}
	dst := filepath.Join(workspace, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(dst, buf, 0o644); err != nil {
		return "", err
	}
	return "/workspace/" + rel, nil
}

var (
	_ Amenity       = (*chromium)(nil)
	_ TenantFactory = (*chromium)(nil)
)

// Close releases only owned contexts in attach mode, then disconnects. Failed
// disposal keeps ownership for retry; the external browser is never stopped.
func (c *chromium) Close(ctx context.Context) error {
	c.mu.Lock()
	c.closed = true
	c.stopIdleTimerLocked()
	clear(c.cdpSecrets)
	var result error
	for id := range c.contexts {
		if err := c.releaseContextLocked(ctx, id); err != nil {
			result = errors.Join(result, err)
		}
	}
	if result != nil {
		c.mu.Unlock()
		return result
	}
	for id, t := range c.tenantHandles {
		t.closed = true
		delete(c.tenantHandles, id)
	}
	c.stopLocked()
	c.state = StateClosed
	c.mu.Unlock()
	c.workers.Wait()
	return nil
}

func (c *chromium) stopIdleTimerLocked() {
	if c.idleTimer != nil {
		if c.idleTimer.Stop() {
			c.workers.Done()
		}
		c.idleTimer = nil
	}
}

// ChromiumStatus describes the shared facility, independently of any Bed.
type ChromiumStatus struct {
	State   string `json:"state"`
	Reason  string `json:"reason,omitempty"`
	Mode    string `json:"mode"`
	Tenants int    `json:"tenants"`
}

func (s ChromiumStatus) lifecycleState() string { return s.State }
