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

package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/amenity"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
	"github.com/qiankunli/hostel/pkg/mcpproxy"
)

func TestMCPBedConfigurationLifecycle(t *testing.T) {
	root := t.TempDir()
	registry := amenity.NewRegistry()
	facility := amenity.NewMCP(mcpproxy.Options{})
	registry.Register(facility)
	mgr, err := bed.NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), registry, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	})
	s := NewServer(mgr)
	body := `{"revision":"v1","servers":{"remote":{"url":"http://127.0.0.1:1"}}}`
	h := map[string]string{BedHeader: "a", "Content-Type": "application/json"}
	out := do(t, s, http.MethodPut, "/v1/mcp/config", strings.NewReader(body), h)
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"changed":true`) {
		t.Fatalf("configure %d %s", out.Code, out.Body.String())
	}
	out = do(t, s, http.MethodPost, "/v1/mcp/servers/remote/tools/list", nil, map[string]string{BedHeader: "b"})
	if out.Code != 404 || !strings.Contains(out.Body.String(), "MCP_SERVER_NOT_FOUND") {
		t.Fatalf("bed b: %d %s", out.Code, out.Body.String())
	}
	// Real bed teardown releases the configured pool, including its secrets.
	previous := facility.Proxy("a")
	out = do(t, s, http.MethodDelete, "/v1/beds/a", nil, nil)
	if out.Code != 200 {
		t.Fatalf("delete %d %s", out.Code, out.Body.String())
	}
	if _, err := previous.Configure(mcpproxy.Bundle{Revision: "x"}); err != mcpproxy.ErrClosed {
		t.Fatal(err)
	}
	out = do(t, s, http.MethodPost, "/v1/mcp/servers/remote/tools/list", nil, h)
	if out.Code != 404 {
		t.Fatalf("restored bed config survived: %d %s", out.Code, out.Body.String())
	}
	out = do(t, s, http.MethodPost, "/v1/mcp/servers/remote/tools/call", strings.NewReader(`{"name":"echo","bundle":{"revision":"a","servers":{"remote":{"url":"http://example","transport":"stdio"}}}}`), h)
	if out.Code != 400 {
		t.Fatalf("unsupported transport: %d %s", out.Code, out.Body.String())
	}
	out = do(t, s, http.MethodPost, "/v1/mcp/unsupported", nil, h)
	if out.Code != 404 {
		t.Fatal(out.Code)
	}
}
