//go:build !linux

package filesystem

import (
	"fmt"
	"os"
	"slices"
)

type currentIdentity struct{}

func (currentIdentity) BindRoot(root Root) (Root, error) { return root.OpenRoot(".") }
func (currentIdentity) Close() error                     { return nil }
func NewIdentityRoots(uid, gid int, groups []int) (IdentityRoots, error) {
	actual, err := os.Getgroups()
	if err != nil {
		return nil, err
	}
	if uid != os.Geteuid() || gid != os.Getegid() {
		return nil, fmt.Errorf("file identity differs from daemon on this platform")
	}
	for _, group := range groups {
		if !slices.Contains(actual, group) && group != gid {
			return nil, fmt.Errorf("file identity supplementary group unavailable")
		}
	}
	return currentIdentity{}, nil
}
