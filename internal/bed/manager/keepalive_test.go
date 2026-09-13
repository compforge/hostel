package manager

import (
	"context"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/bed/store"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

func TestKeepaliveRenewWithoutExpiration(t *testing.T) {
	m, b := renewalBed(t, time.Minute)
	before := b.Status()
	start := time.Now()
	until, err := m.RenewExpiration(b, nil)
	end := time.Now()
	if err != nil {
		t.Fatal(err)
	}
	after := b.Status()
	if after.KeepaliveAt.Before(start) || after.KeepaliveAt.After(end) ||
		!until.Equal(after.KeepaliveAt.Add(time.Minute)) {
		t.Fatalf("keepalive=%s deadline=%s", after.KeepaliveAt, until)
	}
	if !after.LastActiveAt.Equal(before.LastActiveAt) || after.Activity != ActivityIdle || after.Pinned {
		t.Fatal("renew changed activity")
	}
	if m.PinnedBedCount() != 0 || m.InventoryStatus().Instance.Status != InstanceRetained ||
		m.InventoryStatus().Instance.OccupiedBeds != 1 {
		t.Fatal("incorrect retained capacity")
	}
	if got := m.CollectExpired(t.Context(), until.Add(-time.Nanosecond)); len(got) != 0 {
		t.Fatal(got)
	}
	if got := m.CollectExpired(t.Context(), until); len(got) != 1 {
		t.Fatalf("expired bed not collected: %v", got)
	}
}

func TestKeepaliveOperationReservationAndRenewalAreMonotonic(t *testing.T) {
	m, b := renewalBed(t, time.Minute)
	start := time.Now()
	finish, err := m.BeginOperation(b, OpExec, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reserved := b.Status().KeepaliveAt
	if reserved.Before(start.Add(10 * time.Minute)) {
		t.Fatal("operation timeout not reserved")
	}
	until := time.Now().Add(time.Hour)
	if _, err := m.RenewExpiration(b, &until); err != nil {
		t.Fatal(err)
	}
	base := b.Status().KeepaliveAt
	if !base.Equal(until.Add(-time.Minute)) {
		t.Fatal("incorrect renewal base")
	}
	if !b.Status().Pinned || b.Inflight() != 1 {
		t.Fatal("renew changed active operation")
	}
	if got := m.CollectExpired(t.Context(), until.Add(time.Hour)); len(got) != 0 {
		t.Fatal("active bed collected")
	}
	finish()
	if _, err := m.RenewExpiration(b, nil); err != nil {
		t.Fatal(err)
	}
	finishShort, err := m.BeginOperation(b, OpExec, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	finishShort()
	if !b.Status().KeepaliveAt.Equal(base) || !b.RetainUntil().Equal(until) {
		t.Fatal("shorter activity reduced promise")
	}
	if b.Status().Pinned || b.Inflight() != 0 {
		t.Fatal("completed operation remains active")
	}
}

func TestKeepaliveLateOperationCompletionStartsIdleWindow(t *testing.T) {
	m, b := renewalBed(t, time.Minute)
	finish, err := m.BeginOperation(b, OpExec, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	finish()
	end := time.Now()
	status := b.Status()
	if status.KeepaliveAt.Before(start) || status.KeepaliveAt.After(end) ||
		!status.KeepaliveAt.Equal(status.LastActiveAt) {
		t.Fatalf("finish=%+v", status)
	}
	if !status.RetainUntil.Equal(status.KeepaliveAt.Add(time.Minute)) {
		t.Fatal("wrong idle deadline")
	}
}

func TestKeepaliveRenewDoesNotDirtyDurableBed(t *testing.T) {
	root := t.TempDir()
	fs := newFakeStore()
	host := hostfacts.Collect()
	m, err := NewManager(host, root, "default", "/bin/bash", isolation.New(host, "dorm", root), nil, 1, store.NewManagerWithStores(fs))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	m.SetBedIdleTTL(time.Minute)
	b, err := m.Ensure(t.Context(), "durable-renew")
	if err != nil {
		t.Fatal(err)
	}
	before := b.Status()
	if _, err := m.RenewExpiration(b, nil); err != nil {
		t.Fatal(err)
	}
	if synced := m.PersistDirty(t.Context()); len(synced) != 0 {
		t.Fatalf("renew triggered persistence: %v", synced)
	}
	after := b.Status()
	if !after.DataSynced || after.Pinned || m.PinnedBedCount() != 0 ||
		!after.LastActiveAt.Equal(before.LastActiveAt) || after.Generation != before.Generation ||
		fs.generation(b.Name) != 0 {
		t.Fatal("renew changed durability facts")
	}
}
