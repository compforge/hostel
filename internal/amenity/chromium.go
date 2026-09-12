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

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/qiankunli/go-stdx/randx"
	"golang.org/x/sync/semaphore"
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

	// mu protects only in-memory state. No browser I/O or waiting under it.
	mu            sync.Mutex
	runtime       *semaphore.Weighted
	workCtx       context.Context
	cancelWork    context.CancelFunc
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
	// simply sees a fresh browser. notBefore gates startup with
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
	workCtx, cancelWork := context.WithCancel(context.Background())
	return &chromium{workCtx: workCtx, cancelWork: cancelWork, cfg: cfg, attach: cfg.CDPURL != "", runtime: semaphore.NewWeighted(runtimeWeight), state: StateUnavailable, reason: "not_started",
		contexts: make(map[string]*browserContext), tenantHandles: make(map[TenantID]*browserTenant), cdpSecrets: make(map[string]string)}
}

func (c *chromium) Start(ctx context.Context) error {
	if err := c.runtime.Acquire(ctx, runtimeWeight); err != nil {
		return err
	}
	defer c.runtime.Release(runtimeWeight)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("chromium: facility closed")
	}
	if c.state != StateUnavailable {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	reason := ""
	if c.attach {
		if _, err := c.upstreamWSURL(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			reason = "attach_probe_failed"
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
			reason = "executable_not_found"
		} else {
			c.cfg.ExecPath = path
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("chromium: facility closed")
	}
	c.reason = reason
	if reason == "" {
		c.state = StateIdle
	} else {
		log.Printf("amenity: chromium unavailable: %s", reason)
	}
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
	ctx, stopWork := linkedContext(ctx, c.workCtx)
	defer stopWork()
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
	c.mu.Unlock()
	t, release, err := c.acquireTenant(ctx, tenantID, workspace)
	if err != nil {
		return err
	}
	contextID := t.contextID
	release()
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	upstream, err := c.upstreamWSURL(dialCtx)
	cancel()
	if err != nil {
		return err
	}
	return proxyCDP(ctx, conn, upstream, tenantID, string(contextID), onActivity)
}

// ReleaseTenant recycles browser resources without revoking tenant identity.
func (c *chromium) ReleaseTenant(tenantID string) error {
	c.mu.Lock()
	t := c.tenantHandles[TenantID(tenantID)]
	c.mu.Unlock()
	if t == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.ActionTimeout)
	defer cancel()
	return t.CloseContext(ctx)
}

func (c *chromium) run(ctx context.Context, tenantID, workspace string, actions ...chromedp.Action) error {
	ctx, stopWork := linkedContext(ctx, c.workCtx)
	defer stopWork()
	ctx, cancel := context.WithTimeout(ctx, c.cfg.ActionTimeout)
	defer cancel()
	t, release, err := c.acquireTenant(ctx, tenantID, workspace)
	if err != nil {
		return err
	}
	defer release()
	actx, stop := linkedContext(ctx, t.tabCtx)
	defer stop()
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

// ChromiumStatus describes the shared facility, independently of any Bed.
type ChromiumStatus struct {
	State   string `json:"state"`
	Reason  string `json:"reason,omitempty"`
	Mode    string `json:"mode"`
	Tenants int    `json:"tenants"`
}

func (s ChromiumStatus) lifecycleState() string { return s.State }
