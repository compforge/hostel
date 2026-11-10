package bedfs

import (
	"fmt"
	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"
)

// BindIdentity replaces trusted preparation handles before serving requests.
// Subsequent creation needs no daemon chown: the kernel assigns the Bed owner.
func (o *FS) BindIdentity(identity hostfs.IdentityRoots) error {
	root, err := identity.BindRoot(o.root)
	if err != nil {
		return fmt.Errorf("open Bed file identity: %w", err)
	}
	if err := o.root.Close(); err != nil {
		root.Close()
		return err
	}
	o.identity = identity
	o.root = root
	o.uid, o.gid = -1, -1
	for _, mapping := range o.mappings {
		if err := mapping.fs.BindIdentity(identity); err != nil {
			return err
		}
	}
	for _, shared := range o.shared {
		if err := shared.BindIdentity(identity); err != nil {
			return err
		}
	}
	return nil
}
