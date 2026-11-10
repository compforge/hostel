//go:build linux

package filesystem

func NewIdentityRoots(uid, gid int, groups []int) (IdentityRoots, error) {
	return NewUserFiles(uid, gid, groups)
}
