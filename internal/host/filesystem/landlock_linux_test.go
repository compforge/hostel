//go:build linux

package filesystem

import (
	"testing"

	ll "github.com/landlock-lsm/go-landlock/landlock"
)

// +case:id=landlock_abi_floor,desc=`Select enforcement for the available ABI`,expect=`no unsupported or empty restriction can be reported as success`
func TestLandlockConfigRequiresEnforcement(t *testing.T) {
	for _, abi := range []int{-1, 0} {
		if _, err := landlockConfig(abi); err == nil {
			t.Fatalf("ABI %d accepted without enforcement", abi)
		}
	}
	presets := []ll.Config{ll.V1, ll.V2, ll.V3, ll.V4, ll.V5, ll.V6, ll.V7, ll.V8, ll.V9}
	for abi := 1; abi <= 10; abi++ {
		got, err := landlockConfig(abi)
		want := presets[min(abi, len(presets))-1]
		if err != nil || got != want {
			t.Fatalf("ABI %d: config=%v, err=%v; want %v", abi, got, err, want)
		}
	}
}
