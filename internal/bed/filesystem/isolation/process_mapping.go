package isolation

import (
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"
)

// BedFS owns the meaning of workspace and Bed path mappings. Host
// mechanisms receive only resolved source/target paths.
func processMappings(fs *bedfs.FS, support bedfs.MappingSupport) []hostfs.Mapping {
	mappings := []hostfs.Mapping{{Source: fs.Workdir(), Target: bedfs.DefaultWorkdir}}
	for _, m := range fs.PathMappings() {
		if !support.Supports(m.ReadOnly) {
			continue
		}
		mappings = append(mappings, hostfs.Mapping{Source: m.HostPath, Target: m.BedPath})
	}
	return mappings
}
