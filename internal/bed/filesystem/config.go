package filesystem

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/bed/tool"
)

// Config includes process isolation and explicitly authorized Carrier software.
type Config struct {
	isolation.Config
	SharedSoftware []SharedSoftwareDirectory
}

type Options struct {
	DormReadFallbackRoot *string
	Bwrap                *tool.Policy
	Landlock             *tool.Policy
	UID                  *tool.Policy
	PRoot                *tool.Policy
	Pathshim             *tool.Policy
	SharedSoftware       *[]SharedSoftwareDirectory
}

// SharedSoftwareDirectory authorizes instance-wide permission preparation, not
// per-Bed ownership. Only trusted Carrier configuration may grant this authority.
type SharedSoftwareDirectory struct {
	Path   string      `json:"path"`
	GID    int         `json:"gid"`
	Policy tool.Policy `json:"policy,omitempty"`
}

func ValidateSharedSoftware(dirs []SharedSoftwareDirectory) error {
	for i, d := range dirs {
		if !filepath.IsAbs(d.Path) || filepath.Clean(d.Path) != d.Path || d.Path == string(filepath.Separator) {
			return fmt.Errorf("shared software path %q must be a clean absolute directory other than root", d.Path)
		}
		if d.GID <= 0 || uint64(d.GID) >= 4294967295 || (d.GID >= 200000 && d.GID <= 299999) {
			return fmt.Errorf("shared software %s: invalid or reserved GID %d", d.Path, d.GID)
		}
		if err := d.Policy.Validate(); err != nil {
			return fmt.Errorf("shared software %s: %w", d.Path, err)
		}
		for _, other := range dirs[:i] {
			within := func(a, b string) bool {
				r, err := filepath.Rel(a, b)
				return err == nil && (r == "." || (r != ".." && !filepath.IsAbs(r) && !strings.HasPrefix(r, ".."+string(filepath.Separator))))
			}
			if within(d.Path, other.Path) || within(other.Path, d.Path) {
				return fmt.Errorf("shared software paths overlap: %s and %s", d.Path, other.Path)
			}
		}
	}
	return nil
}

func (c Config) Validate() error {
	if err := c.Config.Validate(); err != nil {
		return err
	}
	return ValidateSharedSoftware(c.SharedSoftware)
}
func (c Config) ValidateSharedGroups(groups []int) error {
	for _, d := range c.SharedSoftware {
		if d.Policy.Effective() != tool.Off && !slices.Contains(groups, d.GID) {
			return fmt.Errorf("shared software %s: GID %d is not a Bed supplementary group", d.Path, d.GID)
		}
	}
	return nil
}
