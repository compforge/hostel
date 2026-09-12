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
	"errors"
	"sync"

	"github.com/qiankunli/hostel/pkg/mcpproxy"
)

// MCP owns a remote connection pool per bed. Its idle connections do not
// pin beds; web requests hold bed operations and teardown calls ReleaseAll.
type MCP struct {
	mu      sync.Mutex
	options mcpproxy.Options
	tenants map[string]*mcpproxy.Proxy
}

func NewMCP(options mcpproxy.Options) *MCP {
	return &MCP{options: options, tenants: map[string]*mcpproxy.Proxy{}}
}
func (a *MCP) Name() string { return "mcp" }
func (a *MCP) State() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.tenants) == 0 {
		return StateIdle
	}
	return StateRunning
}
func (a *MCP) AcquireTenant(bedID, workspace string) (Tenant, error) {
	return a.Proxy(bedID), nil
}

// Proxy requires an admitted operation for a live bed. The operation prevents
// a concurrent eviction from recreating a tenant after ReleaseTenant.
func (a *MCP) Proxy(bedID string) *mcpproxy.Proxy {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p := a.tenants[bedID]; p != nil {
		return p
	}
	p := mcpproxy.New(a.options)
	a.tenants[bedID] = p
	return p
}
func (a *MCP) ReleaseTenant(bedID string) error {
	a.mu.Lock()
	p := a.tenants[bedID]
	delete(a.tenants, bedID)
	a.mu.Unlock()
	if p != nil {
		return p.Close()
	}
	return nil
}

// Close stops every remaining connection pool at daemon shutdown.
func (a *MCP) Close(ctx context.Context) error {
	a.mu.Lock()
	ids := make([]string, 0, len(a.tenants))
	for id := range a.tenants {
		ids = append(ids, id)
	}
	a.mu.Unlock()
	var result error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		result = errors.Join(result, a.ReleaseTenant(id))
	}
	return result
}
