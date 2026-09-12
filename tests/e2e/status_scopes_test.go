//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// Status reads expose global domain facts and only the tenants bound to the
// selected Bed. Polling must neither allocate a tenant nor renew Bed activity.
func TestStatusScopesAndTenantIdentity(t *testing.T) {
	target := startTarget(t, targetOptions{isolation: "dorm"})
	c := target.client
	name := "状态测试"
	must2xx(t, "create", c.createBed(t, name))
	c.waitBed(t, name, func(b bedView) bool { return b.Status.Readiness.Ready }, "ready")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	type tenantView struct {
		ID     string `json:"tenant_id"`
		Status struct {
			ConfiguredServers int  `json:"configured_servers"`
			ConnectionEntries int  `json:"connection_entries"`
			Closed            bool `json:"closed"`
		} `json:"status"`
	}
	type detailView struct {
		ID           string    `json:"id"`
		LastActiveAt time.Time `json:"last_active_at"`
		Status       struct {
			Lifecycle  bedLifecycleView           `json:"lifecycle"`
			Components map[string]json.RawMessage `json:"components"`
			Amenities  map[string]tenantView      `json:"amenities"`
		} `json:"status"`
	}
	path := "/v1/beds/" + url.PathEscape(name)
	read := func() detailView {
		t.Helper()
		var d detailView
		response, err := c.json(ctx, http.MethodGet, path, "", nil, &d)
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "detail", response)
		return d
	}
	before := read()
	if before.ID != name || !before.Status.Lifecycle.Readiness.Ready || len(before.Status.Components) != 6 {
		t.Fatalf("detail: %+v", before)
	}
	if _, exists := before.Status.Amenities["mcp"]; exists {
		t.Fatal("status allocated MCP tenant")
	}
	request := map[string]any{"revision": "v1", "servers": map[string]any{"remote": map[string]string{"url": "http://127.0.0.1:1"}}}
	response, err := c.json(ctx, http.MethodPut, "/v1/mcp/config", name, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "configure", response)
	configured := read()
	tenant := configured.Status.Amenities["mcp"]
	if tenant.ID == "" || tenant.Status.ConfiguredServers != 1 || tenant.Status.ConnectionEntries != 0 || tenant.Status.Closed {
		t.Fatalf("MCP domain status: %+v", tenant)
	}
	again := read()
	if again.Status.Amenities["mcp"].ID != tenant.ID || !again.LastActiveAt.Equal(configured.LastActiveAt) {
		t.Fatal("observation changed identity or activity")
	}
	var global struct {
		Schema     int                        `json:"schema_version"`
		Host       map[string]json.RawMessage `json:"host"`
		Components map[string]json.RawMessage `json:"components"`
		Amenities  map[string]struct {
			Tenants int `json:"tenants"`
		} `json:"amenities"`
		Beds []struct {
			Status map[string]json.RawMessage `json:"status"`
		} `json:"beds"`
	}
	response, err = c.json(ctx, http.MethodGet, "/v1/status", "", nil, &global)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "instance status", response)
	if global.Host["runtime"] == nil || global.Host["process"] == nil {
		t.Fatalf("missing host facts: %+v", global.Host)
	}
	if global.Schema != 2 || len(global.Components) != 6 || global.Amenities["mcp"].Tenants != 1 {
		t.Fatalf("global status: %+v", global)
	}
	for _, b := range global.Beds {
		if b.Status["amenities"] != nil || b.Status["components"] != nil {
			t.Fatal("inventory expanded unit details")
		}
	}
	response, err = c.json(ctx, http.MethodDelete, path, name, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "evict", response)
	must2xx(t, "recreate", c.createBed(t, name))
	c.waitBed(t, name, func(b bedView) bool { return b.Status.Readiness.Ready }, "recreated")
	if _, exists := read().Status.Amenities["mcp"]; exists {
		t.Fatal("new Bed inherited binding")
	}
	response, err = c.json(ctx, http.MethodPut, "/v1/mcp/config", name, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "reconfigure", response)
	if read().Status.Amenities["mcp"].ID == tenant.ID {
		t.Fatal("same-name Bed reused Tenant ID")
	}
}
