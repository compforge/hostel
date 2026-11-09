//go:build linux

package filesystem

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"

	"github.com/qiankunli/go-stdx/randx"
	"golang.org/x/sys/unix"
)

// PrepareSharedSoftware grants group access without changing file UIDs. It
// never follows symlinks and refuses multiply-linked regular files before
// mutating them: a link outside the authorized tree shares the same inode.
// +spec=`Only the explicitly authorized tree is changed; directory default ACLs are installed at runtime, before Bed admission.`
// +link=component://docs/isolation.md
func PrepareSharedSoftware(ctx context.Context, path string, gid int) (int, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	// Validate the opened inode, not a pathname checked before opening: an
	// ancestor could otherwise be replaced by a symlink between the two.
	directory, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	actual, readErr := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", directory.Fd()))
	closeErr := directory.Close()
	if readErr != nil {
		return 0, readErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	if actual != path {
		return 0, fmt.Errorf("shared software root must not traverse symlinks: %s resolves to %s", path, actual)
	}
	entries := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			return nil
		}
		f, err := root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		var st unix.Stat_t
		if err := unix.Fstat(int(f.Fd()), &st); err != nil {
			return err
		}
		directory := st.Mode&unix.S_IFMT == unix.S_IFDIR
		if !directory && st.Mode&unix.S_IFMT != unix.S_IFREG {
			return fmt.Errorf("%s changed file type during preparation", name)
		}
		if !directory && st.Nlink > 1 {
			return fmt.Errorf("%s: refusing to change multiply-linked software file", name)
		}
		if !directory && st.Mode&(unix.S_ISUID|unix.S_ISGID) != 0 {
			return fmt.Errorf("%s: privileged executable cannot be shared writable software", name)
		}
		info, err := f.Stat()
		if err != nil {
			return err
		}
		mode := info.Mode() | 0060
		if directory {
			mode |= os.ModeSetgid | 0010
		} else if mode.Perm()&0111 != 0 {
			mode |= 0010
		}
		if int(st.Gid) != gid {
			if err := f.Chown(-1, gid); err != nil {
				return fmt.Errorf("%s group: %w", name, err)
			}
		}
		if info.Mode() != mode || int(st.Gid) != gid {
			if err := f.Chmod(mode); err != nil {
				return fmt.Errorf("%s mode: %w", name, err)
			}
		}
		if directory {
			if err := prepareSharedDefaultACL(f); err != nil {
				return fmt.Errorf("%s default ACL: %w", name, err)
			}
		}
		entries++
		return nil
	})
	if err != nil {
		return entries, fmt.Errorf("prepare shared software %s: %w", path, err)
	}
	if err := verifySharedInheritance(root, gid); err != nil {
		return entries, fmt.Errorf("verify shared software %s: %w", path, err)
	}
	return entries, nil
}

// Linux POSIX ACL xattr format: version 2, then little-endian tag/perm/id
// entries. Group-object refers to each inode's group; no individual Bed UID is
// encoded. The mask permits ordinary group-writable creations with umask 022.
func prepareSharedDefaultACL(f *os.File) error {
	fd := int(f.Fd())
	size, err := unix.Fgetxattr(fd, "system.posix_acl_default", nil)
	var current []byte
	if err == nil {
		current = make([]byte, size)
		n, err := unix.Fgetxattr(fd, "system.posix_acl_default", current)
		if err != nil {
			return err
		}
		current = current[:n]
	} else if !errors.Is(err, unix.ENODATA) {
		return err
	}
	desired, err := sharedDefaultACL(current)
	if err != nil {
		return err
	}
	if bytes.Equal(current, desired) {
		return nil
	}
	if err := unix.Fsetxattr(fd, "system.posix_acl_default", desired, 0); err != nil {
		return err
	}
	actual := make([]byte, len(desired))
	n, err := unix.Fgetxattr(fd, "system.posix_acl_default", actual)
	if err != nil {
		return err
	}
	if n != len(desired) || !bytes.Equal(actual, desired) {
		return fmt.Errorf("default ACL was not retained")
	}
	return nil
}

func sharedDefaultACL(current []byte) ([]byte, error) {
	type entry struct {
		tag, perm uint16
		id        uint32
	}
	var entries []entry
	if len(current) > 0 {
		if len(current) < 4 || (len(current)-4)%8 != 0 || binary.LittleEndian.Uint32(current) != 2 {
			return nil, fmt.Errorf("unsupported POSIX default ACL encoding")
		}
		for off := 4; off < len(current); off += 8 {
			entries = append(entries, entry{binary.LittleEndian.Uint16(current[off:]), binary.LittleEndian.Uint16(current[off+2:]), binary.LittleEndian.Uint32(current[off+4:])})
		}
	}
	// Preserve existing named user/group entries when updating the shared
	// owning group and mask, as setfacl -m would. Do not erase other grants.
	for _, base := range []entry{{1, 7, ^uint32(0)}, {4, 7, ^uint32(0)}, {16, 7, ^uint32(0)}, {32, 5, ^uint32(0)}} {
		found := false
		for i := range entries {
			if entries[i].tag == base.tag {
				entries[i] = base
				found = true
				break
			}
		}
		if !found {
			entries = append(entries, base)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].tag != entries[j].tag {
			return entries[i].tag < entries[j].tag
		}
		return entries[i].id < entries[j].id
	})
	b := make([]byte, 4+len(entries)*8)
	binary.LittleEndian.PutUint32(b, 2)
	for i, e := range entries {
		off := 4 + i*8
		binary.LittleEndian.PutUint16(b[off:], e.tag)
		binary.LittleEndian.PutUint16(b[off+2:], e.perm)
		binary.LittleEndian.PutUint32(b[off+4:], e.id)
	}
	return b, nil
}

func verifySharedInheritance(root *os.Root, gid int) error {
	// A fresh file demonstrates that the mounted filesystem applies the ACL,
	// rather than merely reporting a requested capability or successful write.
	for i := 0; i < 10; i++ {
		name := ".hostel-shared-probe-" + randx.Hex(12)
		f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0666)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		var st unix.Stat_t
		statErr := unix.Fstat(int(f.Fd()), &st)
		closeErr := f.Close()
		removeErr := root.Remove(name)
		if statErr != nil {
			return statErr
		}
		if closeErr != nil {
			return closeErr
		}
		if removeErr != nil {
			return removeErr
		}
		if int(st.Gid) != gid || st.Mode&0060 != 0060 {
			return fmt.Errorf("new file has GID %d mode %#o, need group %d with read/write", st.Gid, st.Mode&0777, gid)
		}
		return nil
	}
	return fmt.Errorf("could not reserve inheritance probe file")
}
