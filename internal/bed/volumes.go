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
// +rule=`Exactly one source is required. Managed owns local data and its persistence policy; HostPath borrows an existing Carrier directory without changing its ownership or deleting it.`
// +spec=`Managed data survives Executor and Pillow replacement; only persistence=sync enters snapshots.`
// +link:doc=docs/volumes.md
type VolumeSpec = protocol.VolumeSpec
type ManagedVolume = protocol.ManagedVolume
type HostPathVolume = protocol.HostPathVolume
type VolumeMount = protocol.VolumeMount

func CloneVolumes(in []VolumeSpec) []VolumeSpec {
	out := slices.Clone(in)
	for i := range out {
		if out[i].HostPath != nil {
			source := *out[i].HostPath
			out[i].HostPath = &source
		}
		if out[i].Managed != nil {
			source := *out[i].Managed
			out[i].Managed = &source
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
		if !volumeName.MatchString(v.Name) || seen[v.Name] || (v.Managed == nil) == (v.HostPath == nil) {
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
		if v.Managed != nil && v.Managed.Persistence != "none" && v.Managed.Persistence != "sync" {
			return nil, fmt.Errorf("%w: volume %s persistence must be none or sync", ErrInvalidPaths, v.Name)
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

// ManagedVolumes is the portable data contract. Host paths and Pillow process
// configuration belong to the local declaration, never the snapshot.
func ManagedVolumes(volumes []VolumeSpec) []VolumeSpec {
	out := make([]VolumeSpec, 0, len(volumes))
	for _, v := range CloneVolumes(volumes) {
		if v.Managed != nil {
			out = append(out, v)
		}
	}
	return out
}

func HasVolume(volumes []VolumeSpec, name string) bool {
	return slices.ContainsFunc(volumes, func(v VolumeSpec) bool { return v.Name == name })
}

// PersistentVolumeRoots selects whole volume allocations, independently of
// Pillow mount targets. Store publishes them in one Bed checkpoint.
func PersistentVolumeRoots(volumes []VolumeSpec) []string {
	roots := make([]string, 0, len(volumes))
	for _, v := range volumes {
		if v.Managed != nil && v.Managed.Persistence == "sync" {
			roots = append(roots, "volumes/"+v.Name)
		}
	}
	return roots
}
