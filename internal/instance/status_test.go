package instance

import (
	"testing"

	"github.com/qiankunli/hostel/internal/amenity"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/bed/manager"
	"github.com/qiankunli/hostel/internal/host/facts"
)

func TestObserverUsesInjectedHostSnapshot(t *testing.T) {
	host := facts.Collect()
	host.System.Runtime.KernelRelease = "injected-host"
	facilities := amenity.NewManager(host)
	root := t.TempDir()
	beds, err := manager.NewManager(host, root, "default", "/bin/sh", isolation.New(host, "dorm", root), facilities, 0, nil)
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
	observer := NewObserver(host, beds, facilities)
	if got := observer.Status().Host.Runtime.KernelRelease; got != "injected-host" {
		t.Fatalf("host snapshot re-probed: %s", got)
	}
	if got := beds.HostFacts().System.Runtime.KernelRelease; got != "injected-host" {
		t.Fatal(got)
	}
}
