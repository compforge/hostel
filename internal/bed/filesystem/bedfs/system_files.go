package bedfs

import (
	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"
	"slices"
)

// SetSystemFiles supplies component-owned, read-only system configuration files
// before the process view is prepared. They are not API data mappings or persisted Bed input.
func (o *FS) SetSystemFiles(files []hostfs.Mapping) { o.systemFiles = slices.Clone(files) }
func (o *FS) SystemFiles() []hostfs.Mapping         { return slices.Clone(o.systemFiles) }
