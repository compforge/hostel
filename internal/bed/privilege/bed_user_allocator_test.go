package privilege

import "testing"

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
	if err != nil || again != a {
		t.Fatalf("same bed allocation = %+v, %v; want %+v", again, err, a)
	}
	if _, err := allocator.Acquire("c"); err == nil {
		t.Fatal("expected exhaustion")
	}
	allocator.Release("a")
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
		if err != nil || got != want {
			t.Fatalf("Acquire(%q) = %+v, %v", id, got, err)
		}
	}
}

func TestBedUserAllocatorReservesExistingOwner(t *testing.T) {
	allocator, err := NewPerBedUserAllocator(200000, 200001)
	if err != nil {
		t.Fatal(err)
	}
	allocator.mu.Lock()
	err = allocator.reserveLocked("cold", 200000)
	allocator.mu.Unlock()
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
	allocator.mu.Lock()
	first := allocator.reserveLocked("a", 200000)
	second := allocator.reserveLocked("b", 200000)
	allocator.mu.Unlock()
	if first != nil || second == nil {
		t.Fatalf("reserve errors = %v, %v", first, second)
	}
}
