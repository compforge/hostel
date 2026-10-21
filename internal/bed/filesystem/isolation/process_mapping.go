package isolation

import (
	"path/filepath"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"
)

// BedFS owns the meaning of workspace and Bed path mappings. Host
// mechanisms receive only resolved source/target paths.
func processMappings(fs *bedfs.FS, support bedfs.MappingSupport) []hostfs.Mapping {
	mappings := []hostfs.Mapping{{Source: fs.Workdir(), Target: fs.ClientWorkdir()}}
	// The built-in temporary path follows the same BedFS as file APIs. Explicit
	// /tmp mappings replace this default; nested mappings retain their precedence.
	temporary := hostfs.Mapping{Source: filepath.Join(fs.Rootfs(), "tmp"), Target: bedfs.TemporaryDir}
	for _, m := range fs.Mounts() {
		if !support.Supports(m.ReadOnly) {
			continue
		}
		if m.Target == bedfs.TemporaryDir {
			temporary.Source = m.Source
			continue
		}
		mappings = append(mappings, hostfs.Mapping{Source: m.Source, Target: m.Target})
	}
	return append([]hostfs.Mapping{temporary}, mappings...)
}
