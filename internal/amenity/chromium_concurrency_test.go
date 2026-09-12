package amenity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// A real CDP transport that accepts commands but never replies reproduces a
// stuck browser without launching Chromium or requiring an E2E environment.
func TestStalledBrowserStartupDoesNotBlockObservation(t *testing.T) {
	for _, finish := range []string{"caller_cancel", "facility_close", "timeout"} {
		t.Run(finish, func(t *testing.T) { testStalledBrowserStartup(t, finish) })
	}
}
func testStalledBrowserStartup(t *testing.T, finish string) {
	received := make(chan struct{})
	var once sync.Once
	var connections atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/version" {
			_ = json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/test"})
			return
		}
		conn, _, _, err := ws.UpgradeHTTP(r, w)
		if err != nil {
			return
		}
		defer conn.Close()
		connections.Add(1)
		for {
			if _, err := wsutil.ReadClientText(conn); err != nil {
				return
			}
			once.Do(func() { close(received) })
		}
	}))
	defer server.Close()
	timeout := 5 * time.Second
	if finish == "timeout" {
		timeout = 500 * time.Millisecond
	}
	c := NewChromium(ChromiumConfig{CDPURL: server.URL, ActionTimeout: timeout})
	if err := c.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c.State() != StateIdle || connections.Load() != 0 {
		t.Fatal("Start eagerly connected browser")
	}
	a, err := c.NewTenant(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.NewTenant(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	caller, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 2)
	workspace := t.TempDir()
	go func() { _, _, err := a.(Browser).Goto(caller, workspace, "about:blank"); done <- err }()
	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("browser command not received")
	}
	go func() { _, _, err := b.(Browser).Goto(caller, workspace, "about:blank"); done <- err }()
	observed := make(chan error, 1)
	go func() {
		_ = c.Status()
		_ = a.Status()
		_, err := b.(Browser).CDPToken()
		observed <- err
	}()
	select {
	case err := <-observed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("status or credentials waited for browser I/O")
	}
	if connections.Load() != 1 {
		t.Fatalf("duplicate shared browser startup: %d", connections.Load())
	}
	switch finish {
	case "caller_cancel":
		cancel()
	case "facility_close":
		closeCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if err := c.Close(closeCtx); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("stalled operation succeeded")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("browser initialization ignored caller cancellation")
		}
	}
	closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := c.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
}

func TestTenantOperationsAreIndependentlySerialized(t *testing.T) {
	c := NewChromium(ChromiumConfig{DebugPort: 9222})
	c.state = StateIdle
	a, _ := c.NewTenant(t.Context())
	b, _ := c.NewTenant(t.Context())
	first := a.(*browserTenant)
	if err := first.operation.Acquire(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	defer first.operation.Release(1)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := a.(Browser).CloseContext(ctx); err == nil {
		t.Fatal("same tenant operation bypassed serialization")
	}
	sibling, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	if err := b.(Browser).CloseContext(sibling); err != nil {
		t.Fatalf("sibling blocked: %v", err)
	}
	if _, err := a.(Browser).CDPToken(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(sibling); err != nil {
		t.Fatal(err)
	}
}
