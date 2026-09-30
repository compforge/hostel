//go:build linux

package network

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qiankunli/go-stdx/shellx"
)

func TestLinuxNetworkTableCleanup(t *testing.T) {
	if os.Getenv("HOSTEL_NETWORK_TEST") != "enabled" {
		t.Skip("requires the disposable Linux network profile")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	backend, probe := probeBackend(ctx)
	if backend == nil || probe.Error != "" {
		t.Fatalf("network prerequisite: %+v", probe)
	}
	b := backend.(*linuxBackend)
	nft := b.nft
	t.Cleanup(func() {
		b.nft = nft
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := b.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	endpoint, err := b.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ep := endpoint.(*linuxEndpoint)
	assertNetworkTables(t, ctx, nft, ep.name, 2)

	// Fail the actual cleanup transaction after its delete statements. Both
	// tables must survive, and the endpoint must retain ownership for retry.
	wrapper := filepath.Join(t.TempDir(), "nft-fail")
	script := fmt.Sprintf("#!/bin/sh\n{ cat; printf 'delete table ip %s_missing\\n'; } | %s -f -\n", ep.name, shellx.Quote(nft))
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	b.nft = wrapper
	if err := ep.Close(ctx); err == nil {
		t.Fatal("expected nft cleanup transaction failure")
	}
	if !ep.table || b.endpoints[ep.name] != ep {
		t.Fatal("failed cleanup lost table ownership")
	}
	assertNetworkTables(t, ctx, nft, ep.name, 2)
	b.nft = nft
	if err := ep.Close(ctx); err != nil {
		t.Fatal(err)
	}
	assertNetworkTables(t, ctx, nft, ep.name, 0)
	if b.endpoints[ep.name] != nil {
		t.Fatal("completed cleanup retained endpoint")
	}
}

func assertNetworkTables(t *testing.T, ctx context.Context, nft, name string, want int) {
	t.Helper()
	out, err := run(ctx, "", nft, "-j", "list", "tables")
	if err != nil {
		t.Fatal(err)
	}
	var rules struct {
		NFTables []struct {
			Table *struct{ Family, Name string }
		}
	}
	if err := json.Unmarshal(out, &rules); err != nil {
		t.Fatal(err)
	}
	families := map[string]bool{}
	for _, entry := range rules.NFTables {
		if entry.Table != nil && entry.Table.Name == name {
			families[entry.Table.Family] = true
		}
	}
	if len(families) != want || (want == 2 && (!families["inet"] || !families["ip"])) {
		t.Fatalf("network tables %s: %v, want %d (inet filter + ip NAT)", name, families, want)
	}
}
