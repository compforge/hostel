package network

import (
	"errors"
	"net"
	"sync"
	"testing"
)

func TestPortsShareConflictDomainAndExactOwner(t *testing.T) {
	m, err := NewPortManager(24000, 24001)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	amenity, err := m.Reserve("amenity/chromium", "", "127.0.0.1:24000")
	if err != nil {
		t.Fatal(err)
	}
	service, err := m.Reserve("bed/a/service/web", "", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	if service.Port() != 24001 {
		t.Fatalf("service reused amenity port: %d", service.Port())
	}
	if _, err := m.Reserve("daemon/http", "", "[::]:24000"); !errors.Is(err, ErrPortsExhausted) {
		t.Fatalf("dual-stack conflict: %v", err)
	}
	private, err := m.Reserve("bed/b/service/web", "bed-b-netns", "0.0.0.0:24000")
	if err != nil {
		t.Fatal(err)
	}
	defer private.Release()
	service.Release()
	replacement, err := m.Reserve("bed/new", "", "0.0.0.0:24001")
	if err != nil {
		t.Fatal(err)
	}
	service.Release() // A delayed cleanup must not remove the replacement.
	if err := replacement.Confirm(); err != nil {
		t.Fatal(err)
	}
	if len(m.Status()) != 3 {
		t.Fatalf("wrong ownership: %+v", m.Status())
	}
	amenity.Release()
}

func TestPortsConcurrentCandidatesAreUnique(t *testing.T) {
	m, _ := NewPortManager(24000, 24099)
	defer m.Close()
	var wg sync.WaitGroup
	addresses := make(chan string, 100)
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := m.Reserve("service", "", "0.0.0.0:0")
			if err != nil {
				t.Error(err)
				return
			}
			addresses <- a.Address()
		}()
	}
	wg.Wait()
	close(addresses)
	seen := make(map[string]bool)
	for address := range addresses {
		if seen[address] {
			t.Fatalf("duplicate allocation: %s", address)
		}
		seen[address] = true
	}
	if _, err := m.Reserve("overflow", "", "0.0.0.0:0"); !errors.Is(err, ErrPortsExhausted) {
		t.Fatalf("exhaustion: %v", err)
	}
}

func TestPortsListenerIsKernelReservation(t *testing.T) {
	m, _ := NewPortManager(24000, 24100)
	defer m.Close()
	a, ln, err := m.Listen("daemon/http", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if other, err := net.Listen("tcp", ln.Addr().String()); err == nil {
		other.Close()
		t.Fatal("listener was not reserved")
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	rebound, err := net.Listen("tcp", a.Address())
	if err != nil {
		t.Fatal(err)
	}
	rebound.Close()
	if err := a.Confirm(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("stale confirmation: %v", err)
	}
}
