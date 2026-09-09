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
	"fmt"
	"sync"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

type connectionKey struct{ revision, digest, server string }
type connection struct {
	gate   chan struct{}
	cancel context.CancelFunc // protected by gate
	client *mcpclient.Client  // protected by gate, never by Proxy.mu
	refs   int                // remaining fields protected by Proxy.mu
	used   time.Time
}

// Proxy owns one sandbox's defaults and sessions. It must not be shared
// between beds. Close revokes its in-flight calls and all idle connections.
type Proxy struct {
	mu             sync.Mutex
	opts           Options
	current        preparedBundle
	connections    map[connectionKey]*connection
	ctx            context.Context
	cancel         context.CancelFunc
	done           chan struct{}
	closeOnce      sync.Once
	ownsHTTPClient bool
}

// New creates a pool and starts its idle reaper. Call Close when the owning
// sandbox ends; supplied HTTP clients remain owned by the embedding process.
func New(opts Options) *Proxy {
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 5 * time.Minute
	}
	if opts.MaxConnections <= 0 {
		opts.MaxConnections = 32
	}
	ownsHTTPClient := opts.HTTPClient == nil
	if ownsHTTPClient {
		opts.HTTPClient = defaultHTTPClient()
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Proxy{opts: opts, ownsHTTPClient: ownsHTTPClient, connections: map[connectionKey]*connection{}, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go p.reap()
	return p
}

// Configure atomically replaces defaults without cancelling in-flight calls.
// Call-scoped revisions remain bounded by idle eviction and MaxConnections.
func (p *Proxy) Configure(bundle Bundle) (bool, error) {
	prepared, err := prepare(bundle, p.opts.MaxConnections)
	if err != nil {
		return false, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ctx.Err() != nil {
		return false, ErrClosed
	}
	changed := p.current.revision != prepared.revision || p.current.digest != prepared.digest
	p.current = prepared
	return changed, nil
}

// ListTools returns all tool pages from the configured remote server.
func (p *Proxy) ListTools(ctx context.Context, server string) (json.RawMessage, error) {
	return p.run(ctx, server, nil, func(ctx context.Context, c *mcpclient.Client) (json.RawMessage, error) {
		result, err := c.ListTools(ctx, mcp.ListToolsRequest{})
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	})
}

// CallTool never retries a dispatched tool. A lost response is ambiguous for
// side-effecting tools; only the next caller may establish a fresh connection.
func (p *Proxy) CallTool(ctx context.Context, server string, call Call) (json.RawMessage, error) {
	if call.Name == "" {
		return nil, fmt.Errorf("%w: tool name required", ErrInvalid)
	}
	var args map[string]any
	if len(call.Arguments) > 0 {
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return nil, fmt.Errorf("%w: arguments must be an object", ErrInvalid)
		}
	}
	return p.run(ctx, server, call.Bundle, func(ctx context.Context, c *mcpclient.Client) (json.RawMessage, error) {
		var meta *mcp.Meta
		if call.Meta != nil {
			meta = mcp.NewMetaFromMap(call.Meta)
		}
		result, err := c.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: call.Name, Arguments: args, Meta: meta}})
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	})
}

func (p *Proxy) run(ctx context.Context, name string, bundle *Bundle, op func(context.Context, *mcpclient.Client) (json.RawMessage, error)) (json.RawMessage, error) {
	p.mu.Lock()
	selected := p.current
	closed := p.ctx.Err() != nil
	p.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	if bundle != nil {
		var err error
		selected, err = prepare(*bundle, p.opts.MaxConnections)
		if err != nil {
			return nil, err
		}
	}
	server, ok := selected.servers[name]
	if !ok {
		return nil, ErrNotFound
	}
	timeout := p.opts.Timeout
	if server.TimeoutMS > 0 && server.TimeoutMS < int(timeout/time.Millisecond) {
		timeout = time.Duration(server.TimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stop := context.AfterFunc(p.ctx, cancel)
	defer stop()
	key := connectionKey{selected.revision, selected.digest, name}
	entry, err := p.acquire(key)
	if err != nil {
		return nil, err
	}
	defer p.release(entry)
	select {
	case entry.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-entry.gate }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if entry.client == nil {
		entry.client, entry.cancel, err = p.connect(ctx, server)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, ErrUpstream
		}
	}
	result, err := op(ctx, entry.client)
	if err != nil {
		if entry.cancel != nil {
			entry.cancel()
			entry.cancel = nil
		}
		_ = entry.client.Close()
		entry.client = nil
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Remote errors may embed headers, query credentials or request bodies.
		// They must not cross the control API or enter ordinary runtime logs.
		return nil, ErrUpstream
	}
	return result, nil
}

func (p *Proxy) acquire(key connectionKey) (*connection, error) {
	p.mu.Lock()
	if p.ctx.Err() != nil {
		p.mu.Unlock()
		return nil, ErrClosed
	}
	if entry := p.connections[key]; entry != nil {
		entry.refs++
		p.mu.Unlock()
		return entry, nil
	}
	var evicted *connection
	if len(p.connections) >= p.opts.MaxConnections {
		var oldestKey connectionKey
		for k, e := range p.connections {
			if e.refs == 0 && (evicted == nil || e.used.Before(evicted.used)) {
				oldestKey = k
				evicted = e
			}
		}
		if evicted == nil {
			p.mu.Unlock()
			return nil, ErrCapacity
		}
		delete(p.connections, oldestKey)
	}
	entry := &connection{gate: make(chan struct{}, 1), refs: 1, used: time.Now()}
	p.connections[key] = entry
	p.mu.Unlock()
	closeConnection(evicted)
	return entry, nil
}

func (p *Proxy) release(entry *connection) {
	p.mu.Lock()
	entry.refs--
	entry.used = time.Now()
	closed := p.ctx.Err() != nil
	p.mu.Unlock()
	if closed {
		closeConnection(entry)
	}
}

func closeConnection(entry *connection) {
	if entry == nil {
		return
	}
	entry.gate <- struct{}{}
	if entry.client != nil {
		if entry.cancel != nil {
			entry.cancel()
			entry.cancel = nil
		}
		_ = entry.client.Close()
		entry.client = nil
	}
	<-entry.gate
}

func (p *Proxy) reap() {
	defer close(p.done)
	ticker := time.NewTicker(p.opts.IdleTimeout)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case now := <-ticker.C:
			var stale []*connection
			p.mu.Lock()
			for key, entry := range p.connections {
				if entry.refs == 0 && now.Sub(entry.used) >= p.opts.IdleTimeout {
					delete(p.connections, key)
					stale = append(stale, entry)
				}
			}
			p.mu.Unlock()
			for _, entry := range stale {
				closeConnection(entry)
			}
		}
	}
}

// Close cancels pending requests and releases connections and configuration.
// It is safe to call concurrently or more than once.
func (p *Proxy) Close() error {
	p.closeOnce.Do(func() {
		p.cancel()
		<-p.done
		p.mu.Lock()
		entries := p.connections
		p.connections = map[connectionKey]*connection{}
		p.current = preparedBundle{}
		p.mu.Unlock()
		for _, entry := range entries {
			closeConnection(entry)
		}
		if p.ownsHTTPClient {
			p.opts.HTTPClient.CloseIdleConnections()
		}
	})
	return nil
}
