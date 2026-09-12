package amenity

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/qiankunli/hostel/internal/bed"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

type testFacility struct {
	mu             sync.Mutex
	name           string
	created        int
	startErr       error
	starts, closes int
	tenants        []*testTenant
}

func (f *testFacility) Name() string                { return f.name }
func (f *testFacility) Start(context.Context) error { f.starts++; return f.startErr }
func (f *testFacility) Close(context.Context) error { f.closes++; return nil }
func (f *testFacility) Status() Status              { return MCPStatus{State: StateIdle} }
func (f *testFacility) NewTenant(context.Context) (Tenant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created++
	t := &testTenant{id: TenantID(fmt.Sprintf("%s-%d", f.name, f.created))}
	f.tenants = append(f.tenants, t)
	return t, nil
}

type testTenant struct {
	id       TenantID
	closes   int
	closeErr error
}

func (t *testTenant) ID() TenantID                { return t.id }
func (t *testTenant) Status() TenantStatus        { return MCPTenantStatus{} }
func (t *testTenant) Close(context.Context) error { t.closes++; return t.closeErr }

func TestBindingsReuseAndRetainFailedCleanup(t *testing.T) {
	m := NewManager(hostfacts.Collect())
	a, b := &testFacility{name: "a"}, &testFacility{name: "b"}
	for _, f := range []*testFacility{a, b} {
		if err := m.Register(f); err != nil {
			t.Fatal(err)
		}
	}
	id := bed.ID("local-identity")
	if err := m.AdmitBed(id); err != nil {
		t.Fatal(err)
	}
	if len(m.BedStatus(id)) != 0 {
		t.Fatal("observation allocated a tenant")
	}
	var wg sync.WaitGroup
	ids := make(chan TenantID, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tenant, err := m.Acquire(t.Context(), id, "a")
			if err != nil {
				t.Error(err)
				return
			}
			ids <- tenant.ID()
		}()
	}
	wg.Wait()
	close(ids)
	for got := range ids {
		if got != "a-1" {
			t.Fatal(got)
		}
	}
	if a.created != 1 {
		t.Fatalf("duplicate tenants: %d", a.created)
	}
	if _, err := m.Acquire(t.Context(), id, "b"); err != nil {
		t.Fatal(err)
	}
	fail := errors.New("release unavailable")
	a.tenants[0].closeErr = fail
	if err := m.ReleaseBed(t.Context(), id); !errors.Is(err, fail) {
		t.Fatal(err)
	}
	state := m.BedStatus(id)
	if len(state) != 1 || state["a"].TenantID != "a-1" {
		t.Fatalf("failed ownership lost: %+v", state)
	}
	if _, err := m.Acquire(t.Context(), id, "a"); err == nil {
		t.Fatal("retiring Bed acquired tenant")
	}
	a.tenants[0].closeErr = nil
	if err := m.ReleaseBed(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if b.tenants[0].closes != 1 {
		t.Fatal("successful release repeated")
	}
	if _, err := m.Acquire(t.Context(), id, "a"); err == nil {
		t.Fatal("forgotten identity reused")
	}
	if err := m.AdmitBed("replacement-id"); err != nil {
		t.Fatal(err)
	}
	next, err := m.Acquire(t.Context(), "replacement-id", "a")
	if err != nil {
		t.Fatal(err)
	}
	if next.ID() == "a-1" {
		t.Fatal("replacement reused identity")
	}
}

func TestFacilityStartupRollbackAndRegistration(t *testing.T) {
	m := NewManager(hostfacts.Collect())
	fail := errors.New("start failed")
	a, b, c := &testFacility{name: "a"}, &testFacility{name: "b", startErr: fail}, &testFacility{name: "c"}
	for _, f := range []*testFacility{a, b, c} {
		if err := m.Register(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Register(&testFacility{name: "a"}); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := m.Start(t.Context()); !errors.Is(err, fail) {
		t.Fatal(err)
	}
	if a.closes != 1 || b.closes != 1 || c.starts != 0 {
		t.Fatalf("rollback: a=%+v b=%+v c=%+v", a, b, c)
	}
	if err := m.AdmitBed("late"); err == nil {
		t.Fatal("failed startup admitted Bed")
	}
	if err := m.Register(&testFacility{name: "late"}); err == nil {
		t.Fatal("late registration accepted")
	}
}

func TestUnavailableChromiumRemainsObservable(t *testing.T) {
	m := NewManager(hostfacts.Collect())
	c := NewChromium(ChromiumConfig{ExecPath: "/nonexistent/hostel-test-chromium"})
	if err := m.Register(c); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	report := m.Status()["chromium"].(ChromiumStatus)
	if report.State != StateUnavailable || report.Reason != "executable_not_found" {
		t.Fatalf("report: %+v", report)
	}
	if err := m.AdmitBed("id"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Browser(t.Context(), "id"); err == nil {
		t.Fatal("unavailable browser allocated tenant")
	}
	if len(m.BedStatus("id")) != 0 {
		t.Fatal("failed acquisition published binding")
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}
