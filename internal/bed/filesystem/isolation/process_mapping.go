package isolation

import (
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"
)

// BedFS owns the meaning of workspace and Bed path mappings. Host
// mechanisms receive only resolved source/target paths.
func processMappings(fs *bedfs.FS) []hostfs.Mapping {
	mappings := []hostfs.Mapping{{Source: fs.Workspace(), Target: bedfs.WorkspacePath}}
	for _, m := range fs.PathMappings() {
		mappings = append(mappings, hostfs.Mapping{Source: m.HostPath, Target: m.BedPath})
	}
	return mappings
}
