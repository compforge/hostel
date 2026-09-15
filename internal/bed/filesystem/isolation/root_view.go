package isolation

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"
)

// runtimePaths are shared execution dependencies, not Bed data. In particular,
// /mnt, /tmp, /home and /run are not inherited from the carrier. Data paths
// outside these overlays belong to the Bed even when first created by a process.
var runtimePaths = []string{
	"/usr", "/bin", "/sbin", "/lib", "/lib64", "/lib32", "/etc", "/opt", "/sys",
}

func existingRuntimePaths() []string {
	var paths []string
	for _, p := range runtimePaths {
		if _, err := os.Stat(p); err == nil {
			paths = append(paths, p)
		}
	}
	return paths
}

func beneath(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+"/")
}

func inRuntime(p string, roots []string) bool {
	for _, root := range roots {
		if beneath(p, root) {
			return true
		}
	}
	return false
}

// rootProcessMappings reuses carrier userland while PRoot's -r owns all other
// absolute paths. PRoot provides path compatibility, not read-only enforcement.
func rootProcessMappings(fs *bedfs.FS, support bedfs.MappingSupport) []hostfs.Mapping {
	var mappings []hostfs.Mapping
	for _, p := range append(existingRuntimePaths(), "/dev", "/proc") {
		mappings = append(mappings, hostfs.Mapping{Source: p, Target: p})
	}
	return append(mappings, processMappings(fs, support)...)
}

// Project Bed-local executables just like cwd. Never bind an arbitrary carrier
// executable into the root: that would bypass sibling masking. Non-system
// carrier tools must be explicitly provided through the Bed's path mappings.
func rootExecutable(fs *bedfs.FS, executable string, support bedfs.MappingSupport) string {
	if !filepath.IsAbs(executable) {
		return executable
	}
	if beneath(executable, fs.Rootfs()) {
		guest, _ := bedfs.RootedView(fs, support).Path(executable)
		return guest
	}
	for _, m := range fs.PathMappings() {
		if support.Supports(m.ReadOnly) && beneath(executable, m.HostPath) {
			return m.BedPath + strings.TrimPrefix(executable, m.HostPath)
		}
	}
	return executable
}
