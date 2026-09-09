//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type mcpAuthKey struct{}

func TestMCPRemoteTools(t *testing.T) {
	for _, transport := range []string{"sse", "streamable_http"} {
		t.Run(transport, func(t *testing.T) {
			remote := server.NewMCPServer("e2e", "1", server.WithToolCapabilities(false))
			remote.AddTool(mcp.NewTool("echo"), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				out := mcp.NewToolResultText(ctx.Value(mcpAuthKey{}).(string))
				out.StructuredContent = map[string]any{"meta": req.Params.Meta}
				return out, nil
			})
			contextFunc := func(ctx context.Context, r *http.Request) context.Context {
				return context.WithValue(ctx, mcpAuthKey{}, r.Header.Get("Authorization"))
			}
			var handler http.Handler
			path := ""
			if transport == "sse" {
				handler = server.NewSSEServer(remote, server.WithSSEContextFunc(contextFunc))
				path = "/sse"
			} else {
				handler = server.NewStreamableHTTPServer(remote, server.WithHTTPContextFunc(contextFunc))
			}
			fixture := httptest.NewServer(handler)
			defer fixture.Close()
			target := startTarget(t, targetOptions{isolation: "dorm", maxBeds: 2})
			c := target.client
			request := func(method, path, bed string, body any) httpResult {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				out, err := c.json(ctx, method, path, bed, body, nil)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			makeBundle := func(token string) map[string]any {
				return map[string]any{"revision": "same-revision", "servers": map[string]any{"remote": map[string]any{"url": fixture.URL + path, "transport": transport, "headers": map[string]string{"Authorization": "Bearer ${TOKEN}"}}}, "secrets": map[string]string{"TOKEN": token}}
			}
			for _, id := range []string{"mcp-a", "mcp-b"} {
				configured := request("PUT", "/v1/mcp/config", id, makeBundle(id))
				must2xx(t, "configure", configured)
				list := request("POST", "/v1/mcp/servers/remote/tools/list", id, nil)
				must2xx(t, "list", list)
				if !strings.Contains(string(list.Body), `"echo"`) {
					t.Fatal(string(list.Body))
				}
			}
			for _, id := range []string{"mcp-a", "mcp-b", "mcp-a"} {
				out := request("POST", "/v1/mcp/servers/remote/tools/call", id, map[string]any{"name": "echo", "arguments": map[string]any{}, "_meta": map[string]string{"session_id": id}})
				must2xx(t, "call", out)
				var result struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
					Structured json.RawMessage `json:"structuredContent"`
				}
				if err := json.Unmarshal(out.Body, &result); err != nil || len(result.Content) != 1 || result.Content[0].Text != "Bearer "+id || !strings.Contains(string(result.Structured), id) {
					t.Fatalf("cross-bed result: %s %v", out.Body, err)
				}
			}
			scoped := request("POST", "/v1/mcp/servers/remote/tools/call", "mcp-a", map[string]any{"name": "echo", "bundle": makeBundle("call-only")})
			must2xx(t, "call-scoped", scoped)
			if !strings.Contains(string(scoped.Body), "Bearer call-only") {
				t.Fatal(string(scoped.Body))
			}
			unchanged := request("POST", "/v1/mcp/servers/remote/tools/call", "mcp-a", map[string]any{"name": "echo"})
			must2xx(t, "default unchanged", unchanged)
			if !strings.Contains(string(unchanged.Body), "Bearer mcp-a") {
				t.Fatal(string(unchanged.Body))
			}
			for _, id := range []string{"mcp-a", "mcp-b"} {
				out := request("DELETE", "/v1/beds/"+id+"?purge=true", "", nil)
				must2xx(t, "purge", out)
			}
			out := request("POST", "/v1/mcp/servers/remote/tools/list", "mcp-a", nil)
			if out.Status != 404 {
				t.Fatalf("configuration survived teardown: %d %s", out.Status, out.Body)
			}
		})
	}
}
