package isolation

import (
	"slices"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	"github.com/qiankunli/hostel/internal/host/filesystem"
)

func TestCommonDirectoryPolicyDrivesMountPermissions(t *testing.T) {
	roots := bedfs.SystemPaths()
	mounts := buildBwrapMounts("/beds", "/beds/a/rootfs", nil, nil, false, roots...)
	for _, p := range roots {
		want := filesystem.Mount{Kind: filesystem.ReadOnlyBind, Source: p, Target: p}
		if !slices.Contains(mounts, want) {
			t.Fatalf("missing read-only system mount %s", p)
		}
	}
	if !slices.Equal(defaultMaskCandidates, bedfs.ProtectedDirectories()) {
		t.Fatal("management path policy drift")
	}
}
