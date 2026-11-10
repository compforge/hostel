package isolation

import (
	"slices"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	"github.com/qiankunli/hostel/internal/host/filesystem"
)

func TestCommonDirectoryPolicyDrivesMountPermissions(t *testing.T) {
	roots := []string{}
	for _, access := range bedfs.CommonDirectories() {
		roots = append(roots, access.Path)
	}
	mounts := buildBwrapMounts("/beds", "/beds/a/rootfs", nil, nil, roots...)
	for _, access := range bedfs.CommonDirectories() {
		found := false
		for _, mount := range mounts {
			if mount.Source == access.Path && mount.Target == access.Path {
				want := filesystem.Bind
				if access.ReadOnly {
					want = filesystem.ReadOnlyBind
				}
				if mount.Kind != want {
					t.Fatalf("path %s: %v != %v", access.Path, mount.Kind, want)
				}
				found = true
			}
		}
		if !found {
			t.Fatalf("missing common path %s", access.Path)
		}
	}
	if !slices.Equal(defaultMaskCandidates, bedfs.ProtectedDirectories()) {
		t.Fatal("management path policy drift")
	}
}
