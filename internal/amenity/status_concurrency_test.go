package amenity

import (
	"context"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/host/facts"
)

type slowTenant struct{ entered, finish chan struct{} }

func (*slowTenant) ID() TenantID         { return "slow" }
func (*slowTenant) Status() TenantStatus { return MCPTenantStatus{} }
func (t *slowTenant) Close(ctx context.Context) error {
	close(t.entered)
	select {
	case <-t.finish:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestBedStatusDoesNotWaitForTenantCleanup(t *testing.T) {
	m := NewManager(facts.Snapshot{})
	if err := m.AdmitBed("local"); err != nil {
		t.Fatal(err)
	}
	tenant := &slowTenant{make(chan struct{}), make(chan struct{})}
	defer close(tenant.finish)
	b := m.binding("local")
	b.tenants["slow"] = tenant
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.ReleaseBed(ctx, "local") }()
	<-tenant.entered
	observed := make(chan map[string]BindingStatus, 1)
	go func() { observed <- m.BedStatus("local") }()
	select {
	case report := <-observed:
		if report["slow"].TenantID != "slow" {
			t.Fatal(report)
		}
	case <-time.After(time.Second):
		t.Fatal("BedStatus waited for remote cleanup")
	}
	cancel()
	<-done
}
