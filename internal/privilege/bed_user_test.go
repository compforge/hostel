package privilege

import "testing"

func TestBedUserRejectsRoot(t *testing.T) {
	for _, test := range []struct{ uid, gid int }{{0, 1000}, {1000, 0}, {-1, 1000}} {
		if _, err := NewBedUser(test.uid, test.gid); err == nil {
			t.Fatalf("NewBedUser(%d, %d) accepted a root/invalid identity", test.uid, test.gid)
		}
	}
}
