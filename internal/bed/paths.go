package bed

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
)

var ErrInvalidPaths = errors.New("bed: invalid path declaration")

// NormalizeSyncPaths preserves omitted versus explicitly empty declarations.
func NormalizeSyncPaths(paths []string) ([]string, error) {
	out := slices.Clone(paths)
	for i, p := range out {
		p = path.Clean(p)
		if !path.IsAbs(p) || p == "/" {
			return nil, fmt.Errorf("%w: sync paths must be absolute and non-root", ErrInvalidPaths)
		}
		for _, other := range out[:i] {
			if PathsOverlap(p, other) {
				return nil, fmt.Errorf("%w: sync paths overlap", ErrInvalidPaths)
			}
		}
		out[i] = p
	}
	slices.Sort(out)
	return out, nil
}

func validateMountPath(p string) error {
	if !path.IsAbs(p) || p == "/" {
		return fmt.Errorf("%w: mount paths must be absolute and non-root", ErrInvalidPaths)
	}
	for _, reserved := range []string{"/workspace", "/dev", "/proc", "/sys", "/.hostel"} {
		if PathsOverlap(p, reserved) {
			return fmt.Errorf("%w: mount overlaps reserved path %s", ErrInvalidPaths, reserved)
		}
	}
	return nil
}

func PathsOverlap(a, b string) bool {
	return a == b || a == "/" || b == "/" || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
