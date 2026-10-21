package bed

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

var ErrInvalidPaths = errors.New("bed: invalid path declaration")

func validateMountPath(p string) error {
	if !path.IsAbs(p) || p == "/" {
		return fmt.Errorf("%w: mount paths must be absolute and non-root", ErrInvalidPaths)
	}
	for _, reserved := range []string{"/dev", "/proc", "/sys", "/.hostel"} {
		if PathsOverlap(p, reserved) {
			return fmt.Errorf("%w: mount overlaps reserved path %s", ErrInvalidPaths, reserved)
		}
	}
	return nil
}

func PathsOverlap(a, b string) bool {
	return a == b || a == "/" || b == "/" || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
