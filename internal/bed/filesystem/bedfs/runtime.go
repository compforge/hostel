package bedfs

import (
	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"
	"slices"
)

// SetRuntimeFiles supplies component-owned, read-only process overlays before
// the view is prepared. They are not API data mappings or persisted Bed input.
func (o *FS) SetRuntimeFiles(files []hostfs.Mapping) { o.runtimeFiles = slices.Clone(files) }
func (o *FS) RuntimeFiles() []hostfs.Mapping         { return slices.Clone(o.runtimeFiles) }
