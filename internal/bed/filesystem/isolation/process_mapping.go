package isolation

import (
	"path/filepath"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"
)

// processMappings compiles defaults and explicit mounts before selecting what
// the mechanism can realize. An unsupported explicit mount must not reappear as
// an implicitly writable default.
// +spec=`Explicit mounts replace default targets exactly once, retain read-only requirements, and preserve nested mappings.`
// +link=component://docs/volumes.md
func processMappings(fs *bedfs.FS, support bedfs.MappingSupport) []hostfs.Mapping {
	explicit := fs.Mounts()
	defaults := []hostfs.Mapping{{Source: filepath.Join(fs.Rootfs(), "tmp"), Target: bedfs.TemporaryDir}}
	// Public workdirs keep their Carrier spelling; /tmp already has a default.
	if beneath(fs.Workdir(), fs.Rootfs()) && fs.ClientWorkdir() != bedfs.TemporaryDir {
		defaults = append(defaults, hostfs.Mapping{Source: fs.Workdir(), Target: fs.ClientWorkdir()})
	}
	var mappings []hostfs.Mapping
	for _, mapping := range defaults {
		overridden := false
		for _, mount := range explicit {
			if beneath(mapping.Target, mount.Target) {
				overridden = true
				break
			}
		}
		if !overridden {
			mappings = append(mappings, mapping)
		}
	}
	for _, m := range explicit {
		if support.Supports(m.ReadOnly) {
			mappings = append(mappings, hostfs.Mapping{Source: m.Source, Target: m.Target})
		}
	}
	return mappings
}
