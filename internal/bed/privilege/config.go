package privilege

import (
	"fmt"
	"slices"
)

// Config grants access to carrier resources independently of Bed identity allocation.
type Config struct{ SupplementaryGIDs []int }
type Options struct{}

// +spec=`Shared resource groups cannot overlap the private Bed identity range.`
// +link=component://docs/isolation.md
func (c *Config) Validate() error {
	c.SupplementaryGIDs = slices.Clone(c.SupplementaryGIDs)
	for _, gid := range c.SupplementaryGIDs {
		if gid <= 0 || uint64(gid) >= 1<<32-1 {
			return fmt.Errorf("invalid extra gid %d", gid)
		}
		if gid >= UIDMin && gid <= UIDMax {
			return fmt.Errorf("extra gid %d overlaps private Bed groups %d..%d", gid, UIDMin, UIDMax)
		}
	}
	slices.Sort(c.SupplementaryGIDs)
	c.SupplementaryGIDs = slices.Compact(c.SupplementaryGIDs)
	return nil
}
