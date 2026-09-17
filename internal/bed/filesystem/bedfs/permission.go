package bedfs

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
)

// ValidatePermission rejects unsupported ownership before any file mutation.
// Directory ownership is allocated by Privilege, never by an HTTP caller.
func (o *FS) ValidatePermission(p string, perm Permission) error {
	_, err := o.route(p, true)
	if err != nil {
		return err
	}
	if perm.Mode < 0 || perm.Mode > 0777 {
		return fmt.Errorf("bedfs: unsupported permission bits")
	}
	uid, gid := o.uid, o.gid
	if uid < 0 || gid < 0 {
		info, err := o.root.Stat(".")
		if err != nil {
			return err
		}
		var ok bool
		uid, gid, ok = ownerOf(info)
		if !ok && (perm.Owner != "" || perm.Group != "") {
			return fmt.Errorf("bedfs: ownership unavailable")
		}
	}
	if perm.Owner != "" && perm.Owner != strconv.Itoa(uid) {
		u, err := user.Lookup(perm.Owner)
		if err != nil || u.Uid != strconv.Itoa(uid) {
			return fmt.Errorf("bedfs: owner must match Bed identity")
		}
	}
	if perm.Group != "" && perm.Group != strconv.Itoa(gid) {
		g, err := user.LookupGroup(perm.Group)
		if err != nil || g.Gid != strconv.Itoa(gid) {
			return fmt.Errorf("bedfs: group must match Bed identity")
		}
	}
	return nil
}

func (o *FS) Open(p string) (*os.File, error) {
	target, err := o.route(p, false)
	if err != nil {
		return nil, err
	}
	full, err := target.Resolve(p)
	if err != nil {
		return nil, err
	}
	rel, err := target.relative(full)
	if err != nil {
		return nil, err
	}
	return target.root.Open(rel)
}
