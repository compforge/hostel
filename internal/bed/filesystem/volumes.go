package filesystem

import (
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	hostprivilege "github.com/qiankunli/hostel/internal/host/privilege"
	"github.com/qiankunli/hostel/protocol"
)

func (m *Manager) PillowFiles(b *bed.Bed) map[string]*bedfs.FS {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := maps.Clone(m.pillows[b])
	if out == nil {
		out = map[string]*bedfs.FS{}
	}
	return out
}

// prepareVolumes resolves sources before publishing any consumer view. Managed sources live in the Bed tree; opening them preserves restored data.
// +link:doc=docs/filesystem.md
func (m *Manager) prepareVolumes(b *bed.Bed, ordinary *bedfs.FS) error {
	spec := b.Spec()
	if err := bed.ValidateVolumeMounts(spec); err != nil {
		return err
	}
	sources, err := resolveHostVolumes(spec)
	if err != nil {
		return err
	}
	hostVolumeCount := len(sources)
	var allocation string
	for _, volume := range spec.Volumes {
		if volume.Managed == nil {
			continue
		}
		if allocation == "" {
			allocation = filepath.Join(spec.Dir, "volumes")
			err = os.MkdirAll(allocation, 0711)
			if err != nil {
				return err
			}
			m.mu.Lock()
			m.volumes[b] = allocation
			m.mu.Unlock()
			// Rootless preparation traverses the parent; each data directory is private.
			if err := os.Chmod(allocation, 0711); err != nil {
				return err
			}
		}
		if err := os.MkdirAll(filepath.Join(allocation, volume.Name), 0700); err != nil {
			return err
		}
		sources[volume.Name] = filepath.Join(allocation, volume.Name)
	}
	mount := func(view *bedfs.FS, pillow bed.PillowSpec) error {
		view.SetWorkdir(pillow.Workdir)
		mounts := make([]bedfs.Mount, 0, len(pillow.Mounts))
		for _, mount := range pillow.Mounts {
			source := sources[mount.Volume]
			owned := allocation != "" && filepath.Dir(source) == allocation
			mounts = append(mounts, bedfs.Mount{Source: source, Target: mount.Path, ReadOnly: mount.ReadOnly, Owned: owned})
		}
		if err := view.SetMounts(mounts); err != nil {
			return err
		}
		return view.EnsureDir(view.Workdir())
	}
	pillows := spec.Pillows
	if len(pillows) == 0 {
		pillows = []bed.PillowSpec{spec.DefaultPillow()}
	}
	m.mu.Lock()
	m.pillows[b] = map[string]*bedfs.FS{}
	m.mu.Unlock()
	for i, pillow := range pillows {
		view := ordinary
		if i > 0 {
			view, err = bedfs.New(ordinary.Rootfs())
			if err != nil {
				return err
			}
		}
		m.mu.Lock()
		m.pillows[b][pillow.Name] = view
		m.mu.Unlock()
		if err := mount(view, pillow); err != nil {
			return err
		}
	}
	log.Printf("filesystem: bed=%s prepared volumes=%d host_volumes=%d pillow_views=%d", b.ID, len(spec.Volumes), hostVolumeCount, len(pillows))
	return nil
}

// Resolve even unmounted sources, and reject aliases before consumers can gain
// conflicting access to the same data through differently named volumes.
func resolveHostVolumes(spec bed.Spec) (map[string]string, error) {
	sources := map[string]string{}
	root, err := filepath.EvalSymlinks(filepath.Dir(spec.Dir))
	if err != nil {
		return nil, err
	}
	for _, volume := range spec.Volumes {
		if volume.HostPath == nil {
			continue
		}
		source, err := filepath.EvalSymlinks(volume.HostPath.Path)
		if err != nil {
			return nil, fmt.Errorf("volume %s source: %w", volume.Name, err)
		}
		info, err := os.Stat(source)
		if err != nil {
			return nil, fmt.Errorf("volume %s source: %w", volume.Name, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("volume %s host_path must be an existing directory", volume.Name)
		}
		if bed.PathsOverlap(root, source) {
			return nil, fmt.Errorf("volume %s source overlaps Hostel beds-root", volume.Name)
		}
		for name, prior := range sources {
			if bed.PathsOverlap(prior, source) {
				return nil, fmt.Errorf("volumes %s and %s sources alias each other", name, volume.Name)
			}
		}
		sources[volume.Name] = source
	}
	return sources, nil
}

func (m *Manager) prepareVolumeOwners(b *bed.Bed) error {
	m.mu.Lock()
	dir := m.volumes[b]
	m.mu.Unlock()
	if dir == "" {
		return nil
	}
	user := b.Status().Privilege
	for _, v := range b.Spec().Volumes {
		if v.Managed == nil {
			continue
		}
		if int(user.UID) != os.Geteuid() || int(user.GID) != os.Getegid() {
			if err := hostprivilege.ChownTree(filepath.Join(dir, v.Name), int(user.UID), int(user.GID)); err != nil {
				return fmt.Errorf("volume %s ownership: %w", v.Name, err)
			}
		}
	}
	return nil
}

func (m *Manager) validateVolumeView(b *bed.Bed, consumer string, fs *bedfs.FS) error {
	spec := b.Spec()
	var mounts []bed.VolumeMount
	for _, pillow := range spec.Pillows {
		if pillow.Name == consumer {
			mounts = pillow.Mounts
			break
		}
	}
	support := m.isolator.View(fs).MappingSupport()
	for _, mount := range mounts {
		status := protocol.VolumeMountStatus{Volume: mount.Volume, Path: mount.Path, Pillow: consumer, ReadOnly: mount.ReadOnly, ProcessMapped: support.Supports(mount.ReadOnly)}
		if !status.ProcessMapped {
			status.Reason = "selected filesystem cannot realize this process mount; file API mapping remains available"
		}
		m.status.Update(b, func(s *bed.FilesystemStatus) { s.Mounts = append(s.Mounts, status) })
		if mount.ReadOnly && !support.ReadOnly {
			return fmt.Errorf("bed %s consumer=%q volume=%s: read-only process mounts unavailable with filesystem %s", b.Name, consumer, mount.Volume, m.isolator.Name())
		}
		log.Printf("filesystem: bed=%s consumer=%q volume=%s path=%s read_only=%t process_mapped=%t", b.ID, consumer, mount.Volume, mount.Path, mount.ReadOnly, support.Supports(mount.ReadOnly))
	}
	return nil
}
