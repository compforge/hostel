package bed

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/qiankunli/hostel/protocol"
)

// VolumeSpec names a data source independently of any consumer's view.
// +rule=`Exactly one source is required. EmptyDir owns temporary data; HostPath borrows an existing Carrier directory without changing its ownership or deleting it.`
// +spec=`EmptyDir data survives Executor and Pillow replacement, is excluded from snapshots, and is removed with the resident allocation.`
// +link:doc=docs/filesystem.md
type VolumeSpec = protocol.VolumeSpec
type EmptyDirVolume = protocol.EmptyDirVolume
type HostPathVolume = protocol.HostPathVolume
type VolumeMount = protocol.VolumeMount

func CloneVolumes(in []VolumeSpec) []VolumeSpec {
	out := slices.Clone(in)
	for i := range out {
		if out[i].HostPath != nil {
			source := *out[i].HostPath
			out[i].HostPath = &source
		}
		if out[i].EmptyDir != nil {
			out[i].EmptyDir = &EmptyDirVolume{}
		}
	}
	return out
}

var volumeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

func NormalizeVolumes(in []VolumeSpec) ([]VolumeSpec, error) {
	out := CloneVolumes(in)
	seen := map[string]bool{}
	for i := range out {
		v := &out[i]
		if !volumeName.MatchString(v.Name) || seen[v.Name] || (v.EmptyDir == nil) == (v.HostPath == nil) {
			return nil, fmt.Errorf("%w: invalid, duplicate or unsupported volume %q", ErrInvalidPaths, v.Name)
		}
		if v.HostPath != nil {
			v.HostPath.Path = filepath.Clean(v.HostPath.Path)
			if !filepath.IsAbs(v.HostPath.Path) || v.HostPath.Path == "/" {
				return nil, fmt.Errorf("%w: volume %s host_path must be absolute and non-root", ErrInvalidPaths, v.Name)
			}
			for _, other := range out[:i] {
				if other.HostPath != nil && PathsOverlap(v.HostPath.Path, other.HostPath.Path) {
					return nil, fmt.Errorf("%w: host_path volumes %s and %s overlap", ErrInvalidPaths, v.Name, other.Name)
				}
			}
		}
		seen[v.Name] = true
	}
	slices.SortFunc(out, func(a, b VolumeSpec) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func NormalizeMounts(in []VolumeMount) ([]VolumeMount, error) {
	out := slices.Clone(in)
	seen := map[string]bool{}

	for i := range out {
		m := &out[i]
		m.Path = path.Clean(m.Path)
		if !volumeName.MatchString(m.Volume) || seen[m.Volume] {
			return nil, fmt.Errorf("%w: invalid or repeated volume mount %q", ErrInvalidPaths, m.Volume)
		}
		seen[m.Volume] = true
		if err := validateMountPath(m.Path); err != nil {
			return nil, err
		}
		for _, other := range out[:i] {
			if PathsOverlap(m.Path, other.Path) {
				return nil, fmt.Errorf("%w: mount paths overlap", ErrInvalidPaths)
			}
		}
	}
	slices.SortFunc(out, func(a, b VolumeMount) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}

// ValidateVolumeMounts checks the complete declaration after local recovery and
// snapshot sync-path resolution. Pillows never inherit ordinary volume grants.
func ValidateVolumeMounts(spec Spec) error {
	names := map[string]bool{}
	for _, v := range spec.Volumes {
		names[v.Name] = true
	}
	check := func(consumer string, mounts []VolumeMount) error {
		for _, m := range mounts {
			if !names[m.Volume] {
				return fmt.Errorf("%w: %s references unknown volume %q", ErrInvalidPaths, consumer, m.Volume)
			}
			for _, syncPath := range spec.SyncPaths {
				if PathsOverlap(m.Path, syncPath) {
					return fmt.Errorf("%w: %s volume mount %s overlaps sync path %s", ErrInvalidPaths, consumer, m.Path, syncPath)
				}
			}
		}
		return nil
	}
	for _, pillow := range spec.Pillows {
		if err := check("pillow "+pillow.Name, pillow.Mounts); err != nil {
			return err
		}
	}
	return nil
}
