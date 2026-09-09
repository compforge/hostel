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

package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type authKey struct{}

func fixture(t *testing.T, kind string, handler server.ToolHandlerFunc) string {
	t.Helper()
	m := server.NewMCPServer("fixture", "1", server.WithToolCapabilities(false))
	m.AddTool(mcp.NewTool("echo", mcp.WithString("value")), handler)
	inject := func(ctx context.Context, r *http.Request) context.Context {
		return context.WithValue(ctx, authKey{}, r.Header.Get("Authorization"))
	}
	var h http.Handler
	suffix := ""
	if kind == "sse" {
		h = server.NewSSEServer(m, server.WithSSEContextFunc(inject))
		suffix = "/sse"
	} else {
		h = server.NewStreamableHTTPServer(m, server.WithHTTPContextFunc(inject))
	}
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s.URL + suffix
}
func echo(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	raw, _ := json.Marshal(req.Params.Meta)
	result := mcp.NewToolResultText(req.GetString("value", "") + ":" + ctx.Value(authKey{}).(string))
	result.StructuredContent = map[string]any{"meta": string(raw)}
	result.IsError = req.GetString("value", "") == "tool-error"
	return result, nil
}
func bundle(endpoint, kind, revision, secret string) Bundle {
	return Bundle{Revision: revision, Servers: map[string]Server{"remote": {URL: endpoint, Transport: kind, Headers: map[string]string{"Authorization": "Bearer ${TOKEN}"}}}, Secrets: map[string]string{"TOKEN": secret}}
}
func invoke(t *testing.T, p *Proxy, b *Bundle, value string) string {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"value": value})
	raw, err := p.CallTool(context.Background(), "remote", Call{Name: "echo", Arguments: args, Bundle: b, Meta: map[string]any{"session_id": "session-a"}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRemoteTransportsAndResult(t *testing.T) {
	for _, kind := range []string{"sse", "streamable_http"} {
		t.Run(kind, func(t *testing.T) {
			endpoint := fixture(t, kind, echo)
			p := New(Options{})
			t.Cleanup(func() { _ = p.Close() })
			b := bundle(endpoint, kind, "a", "secret-a")
			if changed, err := p.Configure(b); err != nil || !changed {
				t.Fatalf("configure %v %v", changed, err)
			}
			if changed, err := p.Configure(b); err != nil || changed {
				t.Fatalf("idempotent configure %v %v", changed, err)
			}
			list, err := p.ListTools(context.Background(), "remote")
			if err != nil || !strings.Contains(string(list), `"echo"`) {
				t.Fatalf("list %s %v", list, err)
			}
			for range 2 {
				raw := invoke(t, p, nil, "hello")
				if !strings.Contains(raw, "hello:Bearer secret-a") || !strings.Contains(raw, "session-a") || !strings.Contains(raw, "structuredContent") {
					t.Fatal(raw)
				}
			}
			raw := invoke(t, p, nil, "tool-error")
			if !strings.Contains(raw, `"isError":true`) {
				t.Fatal(raw)
			}
			p.mu.Lock()
			count := len(p.connections)
			p.mu.Unlock()
			if count != 1 {
				t.Fatalf("connections=%d", count)
			}
		})
	}
}

func TestConcurrentBundlesAndCallerMutation(t *testing.T) {
	endpoint := fixture(t, "streamable_http", echo)
	p := New(Options{})
	t.Cleanup(func() { _ = p.Close() })
	original := bundle(endpoint, "streamable_http", "same-revision", "original")
	if _, err := p.Configure(original); err != nil {
		t.Fatal(err)
	}
	original.Secrets["TOKEN"] = "mutated"
	original.Servers["remote"] = Server{URL: "http://invalid"}
	if raw := invoke(t, p, nil, "default"); !strings.Contains(raw, "Bearer original") {
		t.Fatal(raw)
	}
	var wg sync.WaitGroup
	for _, secret := range []string{"first", "second"} {
		wg.Add(1)
		go func(secret string) {
			defer wg.Done()
			b := bundle(endpoint, "streamable_http", "same-revision", secret)
			raw, err := p.CallTool(context.Background(), "remote", Call{Name: "echo", Bundle: &b})
			if err != nil || !strings.Contains(string(raw), "Bearer "+secret) {
				t.Errorf("result %s %v", raw, err)
			}
		}(secret)
	}
	wg.Wait()
	if raw := invoke(t, p, nil, "default"); !strings.Contains(raw, "Bearer original") {
		t.Fatal(raw)
	}
}

func TestCancellationAndNoReplay(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{}, 1)
	endpoint := fixture(t, "streamable_http", func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	p := New(Options{})
	t.Cleanup(func() { _ = p.Close() })
	b := bundle(endpoint, "streamable_http", "a", "secret")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := p.CallTool(ctx, "remote", Call{Name: "echo", Bundle: &b}); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("call did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel hung")
	}
	if calls.Load() != 1 {
		t.Fatalf("tool replayed %d times", calls.Load())
	}
}

func TestCapacityAndCloseDuringCall(t *testing.T) {
	entered := make(chan struct{}, 1)
	endpoint := fixture(t, "streamable_http", func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	p := New(Options{MaxConnections: 1})
	t.Cleanup(func() { _ = p.Close() })
	a := bundle(endpoint, "streamable_http", "a", "a")
	done := make(chan error, 1)
	go func() {
		_, err := p.CallTool(context.Background(), "remote", Call{Name: "echo", Bundle: &a})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("call did not start")
	}
	b := bundle(endpoint, "streamable_http", "b", "b")
	if _, err := p.CallTool(context.Background(), "remote", Call{Name: "echo", Bundle: &b}); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { _ = p.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close hung")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := p.Configure(a); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestIdleReapingAndMissingConfiguration(t *testing.T) {
	endpoint := fixture(t, "streamable_http", echo)
	p := New(Options{IdleTimeout: 10 * time.Millisecond})
	t.Cleanup(func() { _ = p.Close() })
	if _, err := p.ListTools(context.Background(), "remote"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	b := bundle(endpoint, "streamable_http", "a", "a")
	invoke(t, p, &b, "hello")
	deadline := time.Now().Add(time.Second)
	for {
		p.mu.Lock()
		n := len(p.connections)
		p.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle connection retained")
		}
		time.Sleep(time.Millisecond)
	}
	// Call-scoped configuration is not installed as the default.
	if _, err := p.ListTools(context.Background(), "remote"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestValidationAndUpstreamRedaction(t *testing.T) {
	p := New(Options{})
	t.Cleanup(func() { _ = p.Close() })
	for _, b := range []Bundle{
		{Revision: "a", Servers: map[string]Server{"x": {URL: "http://example", Transport: "stdio"}}},
		{Revision: "a", Servers: map[string]Server{"x": {URL: "http://example", Headers: map[string]string{"Authorization": "${MISSING}"}}}},
		{Revision: "a", Servers: map[string]Server{"x": {URL: "file:///etc/passwd"}}},
	} {
		if _, err := p.Configure(b); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); http.Error(w, "PRIVATE_TOKEN", 401) }))
	defer s.Close()
	b := bundle(s.URL, "streamable_http", "a", "PRIVATE_TOKEN")
	_, err := p.CallTool(context.Background(), "remote", Call{Name: "echo", Bundle: &b})
	if !errors.Is(err, ErrUpstream) || strings.Contains(err.Error(), "PRIVATE_TOKEN") {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("unexpected retries=%d", requests.Load())
	}
}
