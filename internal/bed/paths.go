package bed

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

var ErrInvalidPaths = errors.New("bed: invalid path declaration")

// PathMapping connects carrier data to a Bed path. Hostel does not own or
// synchronize HostPath; its storage owner supplies durability and permissions.
type PathMapping struct {
	HostPath string `json:"host_path"`
	BedPath  string `json:"bed_path"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// NormalizePaths preserves nil (omitted) versus an explicit empty declaration.
// It owns lexical validation; Filesystem checks carrier sources during Prepare.
func NormalizePaths(mappings []PathMapping, syncPaths []string) ([]PathMapping, []string, error) {
	mappings, syncPaths = slices.Clone(mappings), slices.Clone(syncPaths)
	fail := func(message string) ([]PathMapping, []string, error) {
		return nil, nil, fmt.Errorf("%w: %s", ErrInvalidPaths, message)
	}
	for i, p := range syncPaths {
		p = path.Clean(p)
		if !path.IsAbs(p) || p == "/" {
			return fail("sync paths must be absolute and non-root")
		}
		syncPaths[i] = p
		for _, other := range syncPaths[:i] {
			if PathsOverlap(p, other) {
				return fail("sync paths overlap")
			}
		}
	}
	for i, m := range mappings {
		m.HostPath, m.BedPath = filepath.Clean(m.HostPath), path.Clean(m.BedPath)
		if !filepath.IsAbs(m.HostPath) || m.HostPath == "/" || !path.IsAbs(m.BedPath) || m.BedPath == "/" {
			return fail("mapping paths must be absolute and non-root")
		}
		for _, reserved := range []string{"/workspace", "/dev", "/proc", "/sys"} {
			if PathsOverlap(m.BedPath, reserved) {
				return fail("mapping overlaps reserved path " + reserved)
			}
		}
		for _, p := range syncPaths {
			if PathsOverlap(m.BedPath, p) {
				return fail("mapped data cannot be automatically synced")
			}
		}
		for _, other := range mappings[:i] {
			if PathsOverlap(m.BedPath, other.BedPath) || PathsOverlap(m.HostPath, other.HostPath) {
				return fail("path mappings overlap")
			}
		}
		mappings[i] = m
	}
	slices.Sort(syncPaths)
	slices.SortFunc(mappings, func(a, b PathMapping) int { return strings.Compare(a.BedPath, b.BedPath) })
	return mappings, syncPaths, nil
}

func PathsOverlap(a, b string) bool {
	return a == b || a == "/" || b == "/" || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
