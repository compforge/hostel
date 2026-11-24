//go:build linux

package isolation

import (
	"slices"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
)

// +case:id=landlock_common_grants,desc=`Landlock compiles shared and private path grants`,expect=`shared userland is available but Carrier scratch and secrets have no broad parent grant`
func TestLandlockUsesCommonPolicyWithoutGrantingCarrierScratch(t *testing.T) {
	ro, rw := landlockPaths("/tmp/hostel/beds/a/rootfs")
	for _, p := range append(bedfs.SystemPaths(), "/proc", "/sys") {
		if !slices.Contains(ro, p) {
			t.Fatalf("missing read-only path %s", p)
		}
	}
	if !slices.Contains(rw, "/tmp/hostel/beds/a/rootfs") {
		t.Fatal("own data absent")
	}
	for _, path := range append(bedfs.ProtectedDirectories(), "/tmp", "/var/tmp", "/run") {
		if slices.Contains(ro, path) || slices.Contains(rw, path) {
			t.Fatalf("broad management/scratch grant: %s", path)
		}
	}
}
