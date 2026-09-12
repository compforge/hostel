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
	"strings"
	"testing"
	"time"
)

// crashedChromium builds a chromium in the Running state with fake plumbing —
// supervision is a pure state machine over (state, master, tenants), so no
// real browser is needed.
func crashedChromium(tenants ...string) (*chromium, context.Context, context.CancelFunc) {
	c := NewChromium(ChromiumConfig{})
	c.state = StateRunning
	for _, id := range tenants {
		c.contexts[id] = &browserContext{tenantID: id, tabStop: func() {}}
	}
	master, cancel := context.WithCancel(context.Background())
	c.master = master
	c.masterCtl = func() {}
	c.allocStop = func() {}
	return c, master, cancel
}

// TestCrashDropsTenantsAndGates: an unexpected master death must flip the
// amenity to idle, drop every tenant (they hold contexts of a dead browser —
// the next browser action rebuilds resources lazily), and gate the restart.
func TestCrashDropsTenantsAndGates(t *testing.T) {
	c, master, cancel := crashedChromium("bed-a", "bed-b")
	cancel()
	c.onMasterGone(master)

	if c.State() != StateIdle {
		t.Fatalf("state = %s, want idle", c.State())
	}
	c.mu.Lock()
	if len(c.contexts) != 0 {
		t.Fatalf("tenants not dropped: %d left", len(c.contexts))
	}
	if c.crashCount != 1 || !c.notBefore.After(time.Now()) {
		t.Fatalf("gate not armed: count=%d notBefore=%v", c.crashCount, c.notBefore)
	}
	c.mu.Unlock()
	if err := c.startBrowser(t.Context()); err == nil || !strings.Contains(err.Error(), "gated") {
		t.Fatalf("startBrowser during gate = %v, want gated error", err)
	}
}

// TestOrderlyStopIsNotACrash: stopBrowser (idle-stop timer path) cancels the
// master too; the watcher must not count it — state already left Running.
func TestOrderlyStopIsNotACrash(t *testing.T) {
	c, master, cancel := crashedChromium("bed-a")
	c.stopBrowser() // orderly: resources disposed, state → idle
	cancel()
	c.onMasterGone(master)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.crashCount != 0 || !c.notBefore.IsZero() {
		t.Fatalf("orderly stop was counted as crash: count=%d notBefore=%v", c.crashCount, c.notBefore)
	}
}

// TestStaleWatcherIgnored: a watcher from a previous browser instance must not
// touch the current one.
func TestStaleWatcherIgnored(t *testing.T) {
	c, oldMaster, cancel := crashedChromium("bed-a")
	// A new instance replaced the one the watcher observes.
	newMaster, newCancel := context.WithCancel(context.Background())
	defer newCancel()
	c.mu.Lock()
	c.master = newMaster
	c.mu.Unlock()

	cancel()
	c.onMasterGone(oldMaster)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateRunning || c.crashCount != 0 || len(c.contexts) != 1 {
		t.Fatalf("stale watcher mutated current instance: state=%s count=%d tenants=%d",
			c.state, c.crashCount, len(c.contexts))
	}
}

// TestBackoffEscalatesAndResets: rapid crashes escalate the gate exponentially;
// a long-stable run resets the ladder.
func TestBackoffEscalatesAndResets(t *testing.T) {
	c, _, _ := crashedChromium()

	crash := func() time.Duration {
		master, cancel := context.WithCancel(context.Background())
		c.mu.Lock()
		c.master = master
		c.state = StateRunning
		c.notBefore = time.Time{} // the previous gate elapsed
		c.mu.Unlock()
		cancel()
		c.onMasterGone(master)
		c.mu.Lock()
		defer c.mu.Unlock()
		return time.Until(c.notBefore)
	}

	first := crash()
	second := crash()
	if second <= first {
		t.Fatalf("backoff did not escalate: first=%s second=%s", first, second)
	}

	// Stable for >5min → the ladder resets to crash #1 (gate back at the
	// first rung, clearly below the escalated one — exact equality with
	// `first` is timing-jittery).
	c.mu.Lock()
	c.lastCrash = time.Now().Add(-10 * time.Minute)
	c.mu.Unlock()
	reset := crash()
	c.mu.Lock()
	count := c.crashCount
	c.mu.Unlock()
	if count != 1 || reset >= second {
		t.Fatalf("ladder did not reset: count=%d gate=%s (second=%s)", count, reset, second)
	}
}

func TestCrashPreservesTenantIdentityAndCredential(t *testing.T) {
	c := NewChromium(ChromiumConfig{DebugPort: 9222})
	c.state = StateIdle
	handle, err := c.NewTenant(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tenant := handle.(Browser)
	token, err := tenant.CDPToken()
	if err != nil {
		t.Fatal(err)
	}
	master, cancel := context.WithCancel(context.Background())
	c.master, c.masterCtl, c.allocStop, c.state = master, func() {}, func() {}, StateRunning
	c.contexts[tenant.ID().String()] = &browserContext{tabCtx: master, tabStop: func() {}}
	cancel()
	c.onMasterGone(master)
	if c.tenantHandles[tenant.ID()] != handle {
		t.Fatal("crash replaced tenant identity")
	}
	if got, _ := tenant.CDPToken(); got != token {
		t.Fatal("crash revoked tenant credential")
	}
	if state := tenant.Status().(BrowserTenantStatus); state.BrowserReady || state.Closed {
		t.Fatalf("tenant state: %+v", state)
	}
	if err := tenant.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}
