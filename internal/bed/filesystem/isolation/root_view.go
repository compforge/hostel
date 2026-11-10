package isolation

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"
	hostprocess "github.com/qiankunli/hostel/internal/host/process"
)

// Runtime overlays and API routes consume the same BedFS directory policy.
func existingRuntimePaths() []string {
	var paths []string
	for _, access := range bedfs.CommonDirectories() {
		// Writable children are overlaid after their read-only parents.
		if !access.ReadOnly {
			continue
		}
		if _, err := os.Stat(access.Path); err == nil {
			paths = append(paths, access.Path)
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
	for _, access := range bedfs.ProcessDirectories() {
		if _, err := os.Stat(access.Path); err == nil {
			mappings = append(mappings, hostfs.Mapping{Source: access.Path, Target: access.Path})
		}
	}
	mappings = append(mappings, processMappings(fs, support)...)
	return append(mappings, hostfs.Mapping{Source: hostprocess.Executable(), Target: hostprocess.BedInitPath})
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
	for _, m := range fs.Mounts() {
		if support.Supports(m.ReadOnly) && beneath(executable, m.Source) {
			return m.Target + strings.TrimPrefix(executable, m.Source)
		}
	}
	return executable
}
