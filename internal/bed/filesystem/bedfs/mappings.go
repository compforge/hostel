package bedfs

import (
	"fmt"
	"os"
	"path"
	"path/filepath"

	model "github.com/qiankunli/hostel/internal/bed"
)

type mappedRoot struct {
	mapping Mount
	fs      *FS
	owned   bool
}

// Mount is an internal, prepared filesystem binding. Volume owns admission and lifecycle.
// Owned is set only for Hostel-managed volumes, never host_path sources.
type Mount struct {
	Source, Target  string
	ReadOnly, Owned bool
}

// SetMounts binds prepared Volume sources to one Pillow view before publication.
func (o *FS) SetMounts(mounts []Mount) error {
	for _, m := range mounts {
		source, err := filepath.EvalSymlinks(m.Source)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(source) || source == "/" {
			return fmt.Errorf("bedfs: invalid mount source")
		}
		if _, err := model.NormalizeMounts([]model.VolumeMount{{Volume: "prepared", Path: m.Target}}); err != nil {
			return err
		}
		carrierRoot, err := filepath.EvalSymlinks(filepath.Dir(filepath.Dir(o.Rootfs())))
		if err != nil {
			return err
		}
		if !m.Owned && model.PathsOverlap(source, carrierRoot) {
			return fmt.Errorf("bedfs: external mount overlaps Hostel workspace root")
		}
		for _, prior := range o.mappings {
			if model.PathsOverlap(m.Target, prior.mapping.Target) || model.PathsOverlap(source, prior.mapping.Source) {
				return fmt.Errorf("bedfs: mounts overlap")
			}
		}
		child, err := New(source)
		if err != nil {
			return err
		}
		if !m.Owned {
			child.uid, child.gid = -1, -1
		}
		child.paths.prefix, child.paths.workdir, child.readOnly = m.Target, o.paths.workdir, m.ReadOnly
		m.Source = source
		o.mappings = append(o.mappings, mappedRoot{mapping: m, fs: child, owned: m.Owned})
		target, err := o.paths.FromClient(m.Target)
		if err != nil {
			return err
		}
		if err = o.mkdirAllOwned(target); err != nil {
			return err
		}
	}
	return nil
}
func (o *FS) Mounts() []Mount {
	out := make([]Mount, 0, len(o.mappings))
	for _, m := range o.mappings {
		out = append(out, m.mapping)
	}
	return out
}

func (o *FS) clientPath(p string) string {
	if !path.IsAbs(p) {
		return path.Join(o.ClientWorkdir(), p)
	}
	return path.Clean(p)
}
func (o *FS) mapped(p string) *FS {
	p = o.clientPath(p)
	for _, m := range o.mappings {
		if _, ok := relativeTo(m.mapping.Target, p); ok {
			return m.fs
		}
	}
	return nil
}
func (o *FS) route(p string, write bool) (*FS, error) {
	target := o
	if mapped := o.mapped(p); mapped != nil {
		target = mapped
	}
	if write && target.readOnly {
		return nil, &os.PathError{Op: "write read-only mapping", Path: p, Err: os.ErrPermission}
	}
	return target, nil
}

// Destructive tree operations cannot remove or replace a mapping or its parent.
func (o *FS) guardMappingRoot(p string) error {
	p = o.clientPath(p)
	if o.paths.prefix != "" && p == o.paths.prefix {
		return fmt.Errorf("bedfs: cannot replace mapping root")
	}
	for _, m := range o.mappings {
		if _, ok := relativeTo(p, m.mapping.Target); ok {
			return fmt.Errorf("bedfs: operation crosses mapping root %s", m.mapping.Target)
		}
	}
	return nil
}
func (o *FS) transferRoute(p string, write bool) (*FS, error) {
	target, err := o.route(p, write)
	if err != nil {
		return nil, err
	}
	// Transfer archives have a single rooted filesystem. Require the caller to
	// select a mapped tree explicitly, avoiding partial exports/imports at parents.
	if target == o {
		for _, m := range o.mappings {
			if _, ok := relativeTo(o.clientPath(p), m.mapping.Target); ok {
				return nil, fmt.Errorf("bedfs: transfer must select mapping %s explicitly", m.mapping.Target)
			}
		}
	}
	return target, nil
}
