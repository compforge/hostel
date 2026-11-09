package privilege

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Config grants access to carrier resources independently of Bed identity allocation.
type Config struct{ SupplementaryGIDs []int }
type Options struct{ SupplementaryGIDs *[]int }

func ParseExtraGIDs(value string) ([]int, error) {
	groups := []int{}
	if strings.TrimSpace(value) == "" {
		return groups, nil
	}
	for _, part := range strings.Split(value, ",") {
		gid, err := strconv.ParseUint(strings.TrimSpace(part), 10, 32)
		if err != nil || gid == 0 || gid >= 1<<32-1 {
			return nil, fmt.Errorf("invalid extra gid %q: expected a non-root numeric group", part)
		}
		groups = append(groups, int(gid))
	}
	c := Config{SupplementaryGIDs: groups}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c.SupplementaryGIDs, nil
}

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
