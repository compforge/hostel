package isolation

import (
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"
	"path/filepath"
)

// BedFS owns the meaning of workspace and Bed path mappings. Host
// mechanisms receive only resolved source/target paths.
func processMappings(fs *bedfs.FS, support bedfs.MappingSupport) []hostfs.Mapping {
	mappings := []hostfs.Mapping{{Source: fs.Workdir(), Target: bedfs.DefaultWorkdir}}
	// The built-in temporary path follows the same BedFS as file APIs. Explicit
	// /tmp mappings replace this default; nested mappings retain their precedence.
	temporary := hostfs.Mapping{Source: filepath.Join(fs.Rootfs(), "tmp"), Target: bedfs.TemporaryDir}
	for _, m := range fs.PathMappings() {
		if !support.Supports(m.ReadOnly) {
			continue
		}
		if m.BedPath == bedfs.TemporaryDir {
			temporary.Source = m.HostPath
			continue
		}
		mappings = append(mappings, hostfs.Mapping{Source: m.HostPath, Target: m.BedPath})
	}
	return append([]hostfs.Mapping{temporary}, mappings...)
}
