package handler

import (
	"testing"

	"github.com/qiankunli/hostel/internal/amenity"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/bed/manager"
	"github.com/qiankunli/hostel/internal/host/facts"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

func TestStatusObserverUsesInjectedHostSnapshot(t *testing.T) {
	host := facts.Collect()
	host.System.Runtime.KernelRelease = "injected-host"
	facilities := amenity.NewManager(host)
	root := t.TempDir()
	ports, err := hostnetwork.NewPortManager(24000, 24100)
	if err != nil {
		t.Fatal(err)
	}
	defer ports.Close()
	beds, err := manager.NewManager(host, root, "default", "/bin/sh", isolation.New(host, "shared", root), facilities, 0, nil, manager.WithServices(ports, "127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := beds.Close(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	if got := facilities.HostFacts().System.Runtime.KernelRelease; got != "injected-host" {
		t.Fatal(got)
	}
	observer := newStatusObserver(host, beds, facilities)
	if got := observer.Status().Host.Fact.Runtime.KernelRelease; got != "injected-host" {
		t.Fatalf("host snapshot re-probed: %s", got)
	}
	if got := beds.HostFacts().System.Runtime.KernelRelease; got != "injected-host" {
		t.Fatal(got)
	}
	allocation, err := ports.Reserve("test/service", "test-scope", "127.0.0.1:24000")
	if err != nil {
		t.Fatal(err)
	}
	report := observer.Status()
	if got := report.Host.Status.Ports; len(got) != 1 || got[0].Owner != "test/service" || got[0].State != "reserved" {
		t.Fatalf("host port allocation = %+v", got)
	}
	if err := allocation.Release(); err != nil {
		t.Fatal(err)
	}
	if got := observer.Status(); len(got.Host.Status.Ports) != 0 || got.Host.Fact.Runtime.KernelRelease != "injected-host" {
		t.Fatalf("host fact/status lifetime = %+v", got.Host)
	}
	if len(report.Host.Status.Ports) != 1 {
		t.Fatal("subsequent release mutated an earlier status response")
	}
}
