package privilege

import (
	"reflect"
	"testing"
)

func TestBedUserAllocatorAvoidsCollisionsAndReusesRelease(t *testing.T) {
	allocator, err := NewPerBedUserAllocator(200000, 200001)
	if err != nil {
		t.Fatal(err)
	}
	a, err := allocator.Acquire("a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := allocator.Acquire("b")
	if err != nil {
		t.Fatal(err)
	}
	if a.UID() == b.UID() {
		t.Fatalf("different beds share uid %d", a.UID())
	}
	again, err := allocator.Acquire("a")
	if err != nil || !reflect.DeepEqual(again, a) {
		t.Fatalf("same bed allocation = %+v, %v; want %+v", again, err, a)
	}
	if _, err := allocator.Acquire("c"); err == nil {
		t.Fatal("expected exhaustion")
	}
	a.Release()
	c, err := allocator.Acquire("c")
	if err != nil {
		t.Fatal(err)
	}
	if c.UID() != a.UID() {
		t.Fatalf("released uid = %d, reused uid = %d", a.UID(), c.UID())
	}
}

func TestFixedBedUserAllocator(t *testing.T) {
	want, err := NewBedUser(1000, 1001)
	if err != nil {
		t.Fatal(err)
	}
	allocator := NewFixedBedUserAllocator(want)
	for _, id := range []string{"a", "b"} {
		got, err := allocator.Acquire(id)
		if err != nil || !reflect.DeepEqual(got.user, want) {
			t.Fatalf("Acquire(%q) = %+v, %v", id, got, err)
		}
	}
}

func TestBedUserAllocatorReservesExistingOwner(t *testing.T) {
	allocator, err := NewPerBedUserAllocator(200000, 200001)
	if err != nil {
		t.Fatal(err)
	}
	_, err = restoreTestUser(allocator, "cold", 200000, 200000)
	if err != nil {
		t.Fatal(err)
	}
	active, err := allocator.Acquire("active")
	if err != nil {
		t.Fatal(err)
	}
	if active.UID() != 200001 {
		t.Fatalf("active uid=%d, want the unreserved uid 200001", active.UID())
	}
}

func TestBedUserAllocatorRejectsExistingCollision(t *testing.T) {
	allocator, err := NewPerBedUserAllocator(200000, 200001)
	if err != nil {
		t.Fatal(err)
	}
	_, first := restoreTestUser(allocator, "a", 200000, 200000)
	_, second := restoreTestUser(allocator, "b", 200000, 200000)
	if first != nil || second == nil {
		t.Fatalf("reserve errors = %v, %v", first, second)
	}
}

// +case:id=exact_identity_release,desc=`Release an old lease again after reusing the same Bed ID and UID`,expect=`The replacement remains reserved`
func TestBedUserLeaseRejectsStaleRelease(t *testing.T) {
	a, err := NewPerBedUserAllocator(200000, 200000)
	if err != nil {
		t.Fatal(err)
	}
	old, err := a.Acquire("same")
	if err != nil {
		t.Fatal(err)
	}
	old.Release()
	next, err := a.Acquire("same")
	if err != nil {
		t.Fatal(err)
	}
	old.Release()
	if _, err := a.Acquire("other"); err == nil {
		t.Fatal("stale release freed replacement")
	}
	next.Release()
	next.Release()
	if _, err := a.Acquire("other"); err != nil {
		t.Fatal(err)
	}
}

func TestBedUserRestoreRejectsChangedIdentity(t *testing.T) {
	a, err := NewPerBedUserAllocator(200000, 200001)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restoreTestUser(a, "same", 200000, 200000); err != nil {
		t.Fatal(err)
	}
	for _, identity := range [][2]int{{200001, 200001}, {199999, 199999}, {200000, 200001}, {0, 0}} {
		if _, err := restoreTestUser(a, "same", identity[0], identity[1]); err == nil {
			t.Fatalf("accepted %v", identity)
		}
	}
	fixed := NewFixedBedUserAllocator(CurrentBedUser())
	if _, err := restoreTestUser(fixed, "same", CurrentBedUser().UID()+1, CurrentBedUser().GID()); err == nil {
		t.Fatal("changed fixed identity accepted")
	}
}

func restoreTestUser(a *BedUserAllocator, bedID string, uid, gid int) (*BedUserLease, error) {
	if err := a.Restore(bedID, &uid, &gid); err != nil {
		return nil, err
	}
	return a.Acquire(bedID)
}

func TestBedUserAllocatorPreservesIndependentFields(t *testing.T) {
	a, err := NewPerBedUserAllocator(200000, 200002)
	if err != nil {
		t.Fatal(err)
	}
	uid, gid := 200000, 200002
	if err := a.Restore("uid-only", &uid, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Restore("gid-only", nil, &gid); err != nil {
		t.Fatal(err)
	}
	// A complete record deliberately uses different numbers. Neither partial
	// record may take its UID or primary GID while filling its own missing field.
	if _, err := restoreTestUser(a, "complete", 200002, 200000); err != nil {
		t.Fatal(err)
	}
	u, err := a.Acquire("uid-only")
	if err != nil {
		t.Fatal(err)
	}
	g, err := a.Acquire("gid-only")
	if err != nil {
		t.Fatal(err)
	}
	if u.UID() != 200000 || u.GID() != 200001 || g.UID() != 200001 || g.GID() != 200002 {
		t.Fatalf("identities: %d:%d and %d:%d", u.UID(), u.GID(), g.UID(), g.GID())
	}
	if err := a.Restore("duplicate-gid", nil, &gid); err == nil {
		t.Fatal("duplicate GID accepted")
	}
	u.Release()
	g.Release()
	if _, err := restoreTestUser(a, "reuse", 200001, 200002); err != nil {
		t.Fatal(err)
	}
}
