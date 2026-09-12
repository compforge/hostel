package mcpproxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStatusDescribesPoolWithoutExposingConfiguration(t *testing.T) {
	p := New(Options{})
	defer p.Close()
	_, err := p.Configure(Bundle{Revision: "private-revision", Servers: map[string]Server{"private-server": {URL: "http://secret.example"}}})
	if err != nil {
		t.Fatal(err)
	}
	s := p.Status()
	if s.ConfiguredServers != 1 || s.ConnectionEntries != 0 || s.ActiveCalls != 0 || s.Closed {
		t.Fatalf("snapshot: %+v", s)
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "secret") {
		t.Fatal("status exposed configuration")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if !p.Status().Closed {
		t.Fatal("closed pool reported open")
	}
}
