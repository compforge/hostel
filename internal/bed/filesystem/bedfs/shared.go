package bedfs

import (
	"errors"
	model "github.com/qiankunli/hostel/internal/bed"
	"os"
	"path/filepath"
)

// PrepareShared introduces public resources into the file API using the same
// directory policy as the process view. Kernel credentials still decide access;
// writable and read-only sources keep their provider-owned permissions.
func (o *FS) PrepareShared() error {
	if len(o.shared) > 0 {
		return nil
	}
	// More-specific routes precede their parents. Each descriptor remains
	// anchored so a writable source cannot redirect writes into a read-only one.
	carrierRoot, err := filepath.EvalSymlinks(filepath.Dir(filepath.Dir(o.Rootfs())))
	if err != nil {
		return err
	}
	for _, access := range CommonDirectories() {
		target := access.Path
		source, err := filepath.EvalSymlinks(target)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		// A common parent must never become an alternate route into Bed data,
		// especially when the environment can only provide a shared Unix identity.
		if model.PathsOverlap(source, carrierRoot) {
			continue
		}
		child, err := New(source)
		if err != nil {
			return err
		}
		child.paths.prefix = target
		child.paths.workdir = o.paths.workdir
		child.readOnly = access.ReadOnly
		child.common = true
		child.uid, child.gid = -1, -1
		o.shared = append(o.shared, child)
		// Discovery placeholder belongs to private data, never modifies the Carrier.
		placeholder, err := o.paths.FromClient(target)
		if err != nil {
			return err
		}
		if err := o.mkdirAllOwned(placeholder); err != nil {
			return err
		}
	}
	return nil
}

func (o *FS) sharedPath(p string) *FS {
	p = o.clientPath(p)
	for _, shared := range o.shared {
		if _, ok := relativeTo(shared.paths.prefix, p); ok {
			return shared
		}
	}
	return nil
}
