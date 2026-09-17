package network

import (
	"errors"
	"github.com/qiankunli/hostel/internal/bed"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
	"net"
	"testing"
)

func TestPortMappingModesAndOwnership(t *testing.T) {
	ports, err := hostnetwork.NewPortManager(25501, 25600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if len(ports.Status()) != 0 {
			t.Errorf("leaked ports: %+v", ports.Status())
		}
		_ = ports.Close()
	})
	mappings := NewPortMappings(ports, "127.0.0.1")
	makeBed := func(name string) *bed.Bed {
		return bed.New(name, "", bed.Spec{PortMappings: []bed.PortMappingSpec{{Name: "http", Protocol: "tcp", BedPort: 8080, Publish: true}}})
	}
	a, b := makeBed("a"), makeBed("b")
	first, err := mappings.Reserve(a, "http", "web", "", "127.0.0.1", false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := mappings.Reserve(b, "http", "web", "", "127.0.0.1", false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Port() == second.Port() || first.Port() == 8080 || first.Status().Reason != "SharedNetworkUsesHostPort" {
		t.Fatalf("shared allocation: %+v %+v", first.Status(), second.Status())
	}
	first.Bind("execution-one")
	if err := first.Publish(); err != nil {
		t.Fatal(err)
	}
	s := first.Status()
	if s.HostPort != s.BedPort || s.BedAddress != s.HostAddress || s.ExecutionID != "execution-one" {
		t.Fatalf("shared publication: %+v", s)
	}
	if err := first.Withdraw(); err != nil {
		t.Fatal(err)
	}
	if first.Status().HostAddress != "" || mappings.CheckReleased(a) == nil {
		t.Fatal("withdraw released internal ownership")
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	replacement, err := mappings.Reserve(a, "http", "web", "", "127.0.0.1", false)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Release()
	if len(mappings.Status(a)) != 1 {
		t.Fatal("old cleanup deleted replacement")
	}
	_ = replacement.Release()
	_ = second.Release()
	// Logical scopes prove allocator separation, not real Linux namespace entry.
	for _, entry := range []struct {
		b     *bed.Bed
		scope string
	}{{a, "ns-a"}, {b, "ns-b"}} {
		p, err := mappings.Reserve(entry.b, "http", "web", entry.scope, "127.0.0.1", false)
		if err != nil {
			t.Fatal(err)
		}
		if p.Port() != 8080 {
			t.Fatalf("private port=%d", p.Port())
		}
		if err := p.Publish(); err != nil {
			t.Fatal(err)
		}
		if p.Status().HostPort == 0 || p.Status().Network != "private" {
			t.Fatalf("private publication: %+v", p.Status())
		}
		defer func() { _ = p.Release() }()
	}
}
func TestRequiredAndUnpublishedPorts(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	ports, err := hostnetwork.NewPortManager(25601, 25700)
	if err != nil {
		t.Fatal(err)
	}
	defer ports.Close()
	mappings := NewPortMappings(ports, "")
	b := bed.New("fixed", "", bed.Spec{PortMappings: []bed.PortMappingSpec{{Name: "fixed", BedPort: port, RequireBedPort: true}}})
	if _, err := mappings.Reserve(b, "fixed", "server", "", "127.0.0.1", false); !errors.Is(err, hostnetwork.ErrPortsExhausted) {
		t.Fatalf("required port silently changed: %v", err)
	}
	if len(ports.Status()) != 0 {
		t.Fatal("failed allocation leaked")
	}
	b = bed.New("internal", "", bed.Spec{PortMappings: []bed.PortMappingSpec{{Name: "internal", Protocol: "tcp"}}})
	p, err := mappings.Reserve(b, "internal", "worker", "", "127.0.0.1", false)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Release()
	if p.ListenAddress() != p.Status().BedAddress {
		t.Fatalf("unpublished shared listener: %s", p.ListenAddress())
	}
	if err := p.Publish(); err != nil {
		t.Fatal(err)
	}
	if p.Status().HostPort != 0 || p.Status().HostAddress != "" {
		t.Fatal("unpublished mapping exposed external discovery")
	}
}
