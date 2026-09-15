package bedfs

import (
	"fmt"
	"os"
	"path"
	"path/filepath"

	model "github.com/qiankunli/hostel/internal/bed"
)

type mappedRoot struct {
	mapping model.PathMapping
	fs      *FS
}

// SetPathMappings opens existing carrier directories; it never owns, creates or
// changes ownership of external storage. Call before publishing the FS.
func (o *FS) SetPathMappings(mappings []model.PathMapping) error {
	normalized, _, err := model.NormalizePaths(mappings, nil)
	if err != nil {
		return err
	}
	carrierRoot, err := filepath.EvalSymlinks(filepath.Dir(filepath.Dir(o.Rootfs())))
	if err != nil {
		return err
	}
	for _, m := range normalized {
		source, err := filepath.EvalSymlinks(m.HostPath)
		if err != nil {
			return fmt.Errorf("bedfs: mapping source: %w", err)
		}
		if model.PathsOverlap(source, carrierRoot) {
			return fmt.Errorf("bedfs: mapping source overlaps Hostel workspace root")
		}
		for _, previous := range o.mappings {
			if model.PathsOverlap(source, previous.mapping.HostPath) {
				return fmt.Errorf("bedfs: mapping sources alias each other")
			}
		}
		child, err := New(source)
		if err != nil {
			return err
		}
		// External storage permissions belong to its owner, including newly written files.
		child.uid, child.gid = -1, -1
		child.paths.prefix, child.readOnly = m.BedPath, m.ReadOnly
		m.HostPath = source
		o.mappings = append(o.mappings, mappedRoot{m, child})
		// Local data can survive an unredirected process view. Preserve it on
		// reopen; realized mappings hide it and API fallback can still read it.
		target, err := o.paths.FromClient(m.BedPath)
		if err != nil {
			return err
		}
		if err := o.mkdirAllOwned(target); err != nil {
			return err
		}
	}
	return nil
}

func (o *FS) PathMappings() []model.PathMapping {
	result := make([]model.PathMapping, 0, len(o.mappings))
	for _, m := range o.mappings {
		result = append(result, m.mapping)
	}
	return result
}

func clientPath(p string) string {
	if !path.IsAbs(p) {
		return path.Join(DefaultWorkdir, p)
	}
	return path.Clean(p)
}
func (o *FS) mapped(p string) *FS {
	p = clientPath(p)
	for _, m := range o.mappings {
		if _, ok := relativeTo(m.mapping.BedPath, p); ok {
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
	p = clientPath(p)
	if o.paths.prefix != "" && p == o.paths.prefix {
		return fmt.Errorf("bedfs: cannot replace mapping root")
	}
	for _, m := range o.mappings {
		if _, ok := relativeTo(p, m.mapping.BedPath); ok {
			return fmt.Errorf("bedfs: operation crosses mapping root %s", m.mapping.BedPath)
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
			if _, ok := relativeTo(clientPath(p), m.mapping.BedPath); ok {
				return nil, fmt.Errorf("bedfs: transfer must select mapping %s explicitly", m.mapping.BedPath)
			}
		}
	}
	return target, nil
}
