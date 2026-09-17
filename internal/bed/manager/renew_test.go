package manager

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	model "github.com/qiankunli/hostel/internal/bed"
)

func renewalBed(t *testing.T, ttl time.Duration) (*Manager, *Resident) {
	t.Helper()
	m := newTestManager(t)
	m.SetBedIdleTTL(ttl)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	b, err := m.Ensure(t.Context(), "renew")
	if err != nil {
		t.Fatal(err)
	}
	return m, b
}

func TestRenewExpirationOnlyExtendsRetention(t *testing.T) {
	m, b := renewalBed(t, time.Minute)
	before := b.Status()
	until := time.Now().Add(time.Hour).UTC()
	for _, requested := range []time.Time{until, until, until.Add(-time.Minute)} {
		got, err := m.RenewExpiration(b, &requested)
		if err != nil || !got.Equal(until) {
			t.Fatalf("renew = %s, %v", got, err)
		}
	}
	after := b.Status()
	if !before.LastActiveAt.Equal(after.LastActiveAt) || before.DataSynced != after.DataSynced ||
		before.Pinned != after.Pinned || after.Pinned || after.Activity != ActivityIdle || b.Inflight() != 0 {
		t.Fatalf("renew changed activity or pinning: before=%+v after=%+v", before, after)
	}
	if got := m.CollectExpired(t.Context(), before.RetainUntil.Add(time.Second)); len(got) != 0 {
		t.Fatalf("renewed bed reaped: %v", got)
	}
	// Retention is not an operation lease: explicit destruction still works.
	if evicted, err := m.Evict(t.Context(), b.Name); err != nil || !evicted {
		t.Fatalf("explicit eviction = %t, %v", evicted, err)
	}
}

func TestRenewExpirationPreservesUnlimitedRetention(t *testing.T) {
	m, b := renewalBed(t, 0)
	requested := time.Now().Add(time.Hour)
	until, err := m.RenewExpiration(b, &requested)
	if err != nil || !until.IsZero() {
		t.Fatalf("unbounded retention = %s, %v", until, err)
	}
	if got := m.CollectExpired(t.Context(), time.Now().Add(24*time.Hour)); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestRenewExpirationRejectsInvalidAndUnavailableBeds(t *testing.T) {
	m, b := renewalBed(t, time.Minute)
	for _, until := range []time.Time{{}, time.Now().Add(-time.Second), time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)} {
		if _, err := m.RenewExpiration(b, &until); !errors.Is(err, ErrInvalidExpiration) {
			t.Fatalf("invalid deadline: %v", err)
		}
	}
	until := time.Now().Add(time.Hour)
	for _, phase := range []Phase{PhaseEvicting, PhasePurging, PhaseInitializing, PhaseFailed} {
		m.setLifecycle(b.Bed, model.LifecycleStatus{Phase: phase})
		if _, err := m.RenewExpiration(b, &until); !errors.Is(err, ErrBedUnavailable) {
			t.Fatalf("phase %s: %v", phase, err)
		}
	}
	// Readiness is independent: a resident with a restarting service may renew.
	m.setLifecycle(b.Bed, model.LifecycleStatus{Phase: PhaseResident, Ready: false})
	if _, err := m.RenewExpiration(b, &until); err != nil {
		t.Fatal(err)
	}
	if b.Bed.Status().Lifecycle.Ready {
		t.Fatal("renew changed readiness")
	}
	if _, err := m.Evict(t.Context(), b.Name); err != nil {
		t.Fatal(err)
	}
	replacement, err := m.Ensure(t.Context(), b.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.RenewExpiration(b, &until); !errors.Is(err, ErrBedUnavailable) {
		t.Fatalf("stale handle accepted: %v", err)
	}
	if replacement.RetainUntil().Equal(until) {
		t.Fatal("stale renewal affected replacement")
	}
}

func TestRenewExpirationFencesIdleCollection(t *testing.T) {
	for i := 0; i < 12; i++ {
		t.Run(time.Duration(i).String(), func(t *testing.T) {
			m, b := renewalBed(t, time.Minute)
			cutoff := time.Now()
			b.mu.Lock()
			m.owners.Lifecycle.Update(b.Bed, func(s *model.LifecycleStatus) { s.KeepaliveAt = cutoff.Add(-b.idleTTL - time.Second) })
			b.mu.Unlock()
			var renewed, evicted bool
			var renewErr, evictErr error
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				until := cutoff.Add(time.Hour)
				_, renewErr = m.RenewExpiration(b, &until)
				renewed = renewErr == nil
			}()
			go func() { defer wg.Done(); evicted, evictErr = m.evictExpired(t.Context(), b.Name, cutoff) }()
			wg.Wait()
			if evictErr != nil && !errors.Is(evictErr, ErrBedUnavailable) {
				t.Fatal(evictErr)
			}
			if renewed && evicted {
				t.Fatal("idle collection destroyed a successfully renewed bed")
			}
			if renewErr != nil && !errors.Is(renewErr, ErrBedUnavailable) {
				t.Fatal(renewErr)
			}
		})
	}
}

func TestRenewExpirationKeepsServiceExecution(t *testing.T) {
	m, specs, _ := testServiceManager(t)
	m.SetBedIdleTTL(time.Minute)
	if _, err := m.InitializeBedWithOptions(t.Context(), "renew-service", testServiceOptions(specs)); err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(t.Context(), "renew-service")
	if err != nil {
		t.Fatal(err)
	}
	before := m.services.Status(b.Bed)
	until := time.Now().Add(time.Hour)
	if _, err := m.RenewExpiration(b, &until); err != nil {
		t.Fatal(err)
	}
	after := m.services.Status(b.Bed)
	if len(after) != len(before) || b.Inflight() != 0 || b.Status().Pinned {
		t.Fatal("renew changed service or operation state")
	}
	for i := range before {
		if before[i].ExecutionID != after[i].ExecutionID {
			t.Fatal("renew restarted service")
		}
	}
	if reaped := m.CollectExpired(t.Context(), time.Now().Add(2*time.Minute)); len(reaped) != 0 {
		t.Fatal(reaped)
	}
}
