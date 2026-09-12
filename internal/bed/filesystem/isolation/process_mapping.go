package isolation

import (
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"
)

// BedFS owns the meaning of workspace and configured projections. Host
// mechanisms receive only resolved source/target paths.
func processMappings(fs *bedfs.FS, projections []bedfs.PathProjection) []hostfs.Mapping {
	mappings := []hostfs.Mapping{{Source: fs.Workspace(), Target: bedfs.WorkspacePath}}
	for _, p := range projections {
		mappings = append(mappings, hostfs.Mapping{Source: p.CarrierPath(fs.Home()), Target: p.ProcessPath})
	}
	return mappings
}
