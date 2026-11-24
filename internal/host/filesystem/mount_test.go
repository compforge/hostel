package filesystem

import (
	"slices"
	"testing"
)

func TestBubblewrapRemountTakesOnlyTarget(t *testing.T) {
	b := Bubblewrap{Mounts: []Mount{{Kind: RemountReadOnly, Target: "/"}}}
	want := []string{"--remount-ro", "/", "--"}
	if got := b.Args(); !slices.Equal(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}
