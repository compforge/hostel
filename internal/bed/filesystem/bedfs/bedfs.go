// Copyright 2026 Li Qiankun
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package bedfs owns one bed's filesystem identity and operations. A BedFS is
// rooted at bed_home: it defines the client namespace, carrier placement and
// Executor views independently of any particular isolation mechanism.
//
// Rootfs owns the Bed-local data root; Mounts connect declared Volume
// data. Workdir is the default directory and relative API path base. These
// data addresses are independent of the process view and its isolation level.
package bedfs

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"
)

// FileInfo describes BedFS metadata using Unix permission bits and Bed paths.
// Protocol adapters own their external field encodings.
type FileInfo struct {
	Path       string    `json:"path,omitempty"`
	Type       string    `json:"type,omitempty"` // "file" | "directory" | "symlink"
	Size       int64     `json:"size"`
	CreatedAt  time.Time `json:"created_at,omitzero"`
	ModifiedAt time.Time `json:"modified_at,omitzero"`
	Owner      string    `json:"owner"`
	Group      string    `json:"group"`
	Mode       int       `json:"mode"`
}

// ReplaceItem / ReplaceResult describe a literal content substitution.
type ReplaceItem struct {
	Old string `json:"old,omitempty"`
	New string `json:"new,omitempty"`
}
type ReplaceResult struct {
	ReplacedCount int `json:"replacedCount"`
}

// RenameItem describes a move within the authorized filesystem.
type RenameItem struct {
	Src  string `json:"src,omitempty"`
	Dest string `json:"dest,omitempty"`
}

// Permission uses Unix mode bits. Ownership must match the Bed-assigned identity.
type Permission struct {
	Owner string `json:"owner"`
	Group string `json:"group"`
	Mode  int    `json:"mode"`
}

// FS is one bed's filesystem. It is created with the Bed and survives
// Executor replacement; all daemon file operations are confined to it.
//
// +spec=`File and directory APIs preserve client path spelling while each operation stays within private data, a declared mapping source, or an internally shared source.`
// +case:id=filesystem_api_contract,desc=`Create, inspect, replace, chmod, move, search, slice-read, and delete a workspace tree`,expect=`all operations round-trip through the public client path without escaping the BedFS`
type FS struct {
	identity    hostfs.IdentityRoots
	shared      []*FS
	systemFiles []hostfs.Mapping
	mappings    []mappedRoot
	readOnly    bool
	common      bool // Public source; creation follows the common-directory policy.
	paths       paths
	root        hostfs.Root
	// uid/gid of the workspace dir when it differs from the daemon's euid
	// (uid-isolated beds), else -1. Mechanism-independent invariant: whatever
	// lands in a bed's workspace belongs to the bed — BedFS runs as the
	// daemon, so without this, file-API writes would leave daemon-owned files
	// the bed can read but not modify (docs/isolation.md, room/uid).
	uid, gid int
}

// New returns file ops confined to home (the bed_home host directory).
// File ops never need the mount view, so the embedded Paths carries no mount
// point; exec-side callers use the bed's own Paths for that.
func New(root string) (*FS, error) {
	confined, err := hostfs.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("bedfs: open root %q: %w", root, err)
	}
	o := &FS{paths: newPaths(root), root: confined, uid: -1, gid: -1}
	if err := o.RefreshOwner(); err != nil {
		_ = confined.Close()
		return nil, err
	}
	return o, nil
}

// RefreshOwner refreshes the owner used for subsequent daemon file operations.
// Call only during preparation, before publishing the Bed: UID isolation changes
// the root owner after New opens its descriptor.
func (o *FS) RefreshOwner() error {
	fi, err := o.root.Stat(".")
	if err != nil {
		return err
	}
	for _, mapping := range o.mappings {
		if mapping.owned {
			if err := mapping.fs.RefreshOwner(); err != nil {
				return err
			}
		}
	}
	o.uid, o.gid = -1, -1
	if uid, gid, ok := ownerOf(fi); ok && uid != os.Geteuid() {
		o.uid, o.gid = uid, gid
	}
	return nil
}

// Close releases the descriptor anchoring this BedFS. A Bed keeps it open for
// its resident lifetime so renames and symlink swaps cannot redirect daemon
// file operations outside bed_home.
func (o *FS) Close() error {
	err := o.root.Close()
	for _, shared := range o.shared {
		err = errors.Join(err, shared.Close())
	}
	for _, m := range o.mappings {
		err = errors.Join(err, m.fs.Close())
	}
	return err
}

func (o *FS) relative(full string) (string, error) {
	rel, err := filepath.Rel(o.Rootfs(), full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("bedfs: carrier path %q is outside bed_home %q", full, o.Rootfs())
	}
	return rel, nil
}

// chownNew hands a path BedFS just created over to the workspace owner.
// Best-effort: if the daemon lacks CAP_CHOWN the file stays daemon-owned,
// which is the pre-invariant behavior (bed reads it, next Prepare re-chowns).
// Same hardlink discipline as privilege's ownership handoff: a multiply-linked file is
// a second name for an inode possibly outside the workspace — rehoming it
// would gift the bed ownership of a host file, so leave it alone. Lchown so a
// symlink planted at the path can't redirect the chown to its referent.
func (o *FS) chownNew(full string) {
	if o.uid < 0 {
		return
	}
	rel, err := o.relative(full)
	if err != nil {
		return
	}
	fi, err := o.root.Lstat(rel)
	if err != nil {
		return
	}
	if fi.Mode().IsRegular() && nlinkOf(fi) > 1 {
		return
	}
	_ = o.root.Lchown(rel, o.uid, o.gid)
}

// mkdirAllOwned is MkdirAll + owner handover on the directories that need it.
// It fixes owners AFTER creating (walk deepest→root, chown while the owner is
// wrong, stop at the first correctly-owned ancestor) rather than pre-probing
// which levels are missing: a pre-probe races with concurrent deletes (an
// ancestor deleted between probe and MkdirAll would be recreated daemon-owned
// and skipped), and the post-walk also heals daemon-owned dirs left by older
// hostel versions. Directories can't be hardlinked, so rehoming is safe.
func (o *FS) mkdirAllOwned(dir string) error {
	rel, err := o.relative(dir)
	if err != nil {
		return err
	}
	mode := os.FileMode(0755)
	if o.common {
		mode = 0770
	}
	if err := o.root.MkdirAll(rel, mode); err != nil {
		return err
	}
	if o.uid < 0 {
		return nil
	}
	for d := dir; strings.HasPrefix(d, o.Rootfs()); d = filepath.Dir(d) {
		rel, err := o.relative(d)
		if err != nil {
			break
		}
		fi, err := o.root.Lstat(rel)
		if err != nil {
			break
		}
		if uid, _, ok := ownerOf(fi); !ok || uid == o.uid {
			break
		}
		_ = o.root.Lchown(rel, o.uid, o.gid)
	}
	return nil
}

// EnsureDir creates host directory dir (and any missing parents) with the
// workspace's owner, for callers that resolved a host path via Resolve and need
// it to exist — e.g. an exec cwd the caller named but hasn't created yet. dir
// must already be bed_home-confined (Resolve guarantees this); ownership
// handover keeps it usable under the uid isolation tier.
func (o *FS) EnsureDir(dir string) error {
	for _, m := range o.mappings {
		if _, ok := relativeTo(m.fs.Rootfs(), dir); ok {
			return m.fs.EnsureDir(dir)
		}
	}
	if _, private := relativeTo(o.Rootfs(), dir); !private {
		for _, shared := range o.shared {
			if _, ok := relativeTo(shared.Rootfs(), dir); ok {
				return shared.EnsureDir(dir)
			}
		}
	}
	if o.readOnly {
		rel, err := o.relative(dir)
		if err != nil {
			return err
		}
		info, err := o.root.Stat(rel)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("bedfs: cwd is not a directory")
		}
		return nil
	}
	return o.mkdirAllOwned(dir)
}

// Resolve maps a client path to a carrier path under bed_home. It is shared by
// file operations and structured Executor paths such as cwd.
func (o *FS) Resolve(p string) (string, error) {
	if mapped := o.mapped(p); mapped != nil {
		return mapped.paths.FromClient(p)
	}
	if shared := o.sharedPath(p); shared != nil {
		return shared.paths.FromClient(p)
	}
	return o.paths.FromClient(p)
}

// Rootfs returns the Carrier data root backing this Bed's logical /.
// It does not claim that the process root has been replaced.
func (o *FS) Rootfs() string { return o.paths.Rootfs() }

// Workdir returns the default working directory in Carrier coordinates.
func (o *FS) Workdir() string { p, _ := o.Resolve(o.ClientWorkdir()); return p }

func (o *FS) virtual(full string) string { return o.paths.ToClient(full) }

func (o *FS) info(full string, li os.FileInfo) FileInfo {
	typ := "other"
	switch {
	case li.Mode().IsRegular():
		typ = "file"
	case li.IsDir():
		typ = "directory"
	case li.Mode()&os.ModeSymlink != 0:
		typ = "symlink"
	}
	owner, group := "", ""
	if uid, gid, ok := ownerOf(li); ok {
		owner = strconv.Itoa(uid)
		group = strconv.Itoa(gid)
		if u, err := user.LookupId(owner); err == nil {
			owner = u.Username
		}
		if g, err := user.LookupGroupId(group); err == nil {
			group = g.Name
		}
	}
	return FileInfo{
		Owner: owner, Group: group, CreatedAt: creationTime(li),
		Path:       o.virtual(full),
		Type:       typ,
		Size:       li.Size(),
		ModifiedAt: li.ModTime(),
		Mode:       int(li.Mode().Perm()),
	}
}

// Stat returns metadata for one path.
func (o *FS) Stat(p string) (FileInfo, error) {
	o, routeErr := o.route(p, false)
	if routeErr != nil {
		return FileInfo{}, routeErr
	}
	full, err := o.Resolve(p)
	if err != nil {
		return FileInfo{}, err
	}
	rel, err := o.relative(full)
	if err != nil {
		return FileInfo{}, err
	}
	li, err := o.root.Lstat(rel)
	if err != nil {
		return FileInfo{}, err
	}
	return o.info(full, li), nil
}

// Read returns full file contents.
func (o *FS) Read(p string) ([]byte, error) {
	o, routeErr := o.route(p, false)
	if routeErr != nil {
		return nil, routeErr
	}
	full, err := o.Resolve(p)
	if err != nil {
		return nil, err
	}
	rel, err := o.relative(full)
	if err != nil {
		return nil, err
	}
	return o.root.ReadFile(rel)
}

// ReadLines returns up to limit lines starting at 0-based line offset.
func (o *FS) ReadLines(p string, offset, limit int) (string, error) {
	data, err := o.Read(p)
	if err != nil {
		return "", err
	}
	lines := strings.SplitAfter(string(data), "\n")
	// SplitAfter leaves a trailing "" when the file ends with \n.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if offset >= len(lines) {
		return "", nil
	}
	end := len(lines)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return strings.Join(lines[offset:end], ""), nil
}

// Write creates/overwrites a file (0644 when mode==0), making parent dirs.
func (o *FS) Write(p string, data []byte, mode int) error {
	o, routeErr := o.route(p, true)
	if routeErr != nil {
		return routeErr
	}
	full, err := o.Resolve(p)
	if err != nil {
		return err
	}
	if err := o.mkdirAllOwned(filepath.Dir(full)); err != nil {
		return err
	}
	fm := os.FileMode(0o644)
	if o.common {
		fm = 0660
	}
	if mode != 0 {
		fm = os.FileMode(mode) & os.ModePerm
	}
	rel, err := o.relative(full)
	if err != nil {
		return err
	}
	if err := o.root.WriteFile(rel, data, fm); err != nil {
		return err
	}
	o.chownNew(full)
	return nil
}

// Remove deletes files (not directories); missing files are ignored.
func (o *FS) Remove(paths []string) error {
	for _, p := range paths {
		if err := o.guardMappingRoot(p); err != nil {
			return err
		}
		target, err := o.route(p, true)
		if err != nil {
			return err
		}
		if target != o {
			if err := target.Remove([]string{p}); err != nil {
				return err
			}
			continue
		}
		full, err := o.Resolve(p)
		if err != nil {
			return err
		}
		rel, err := o.relative(full)
		if err != nil {
			return err
		}
		li, err := o.root.Lstat(rel)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if li.IsDir() {
			return fmt.Errorf("bedfs: %q is a directory (use the directories API)", p)
		}
		if err := o.root.Remove(rel); err != nil {
			return err
		}
	}
	return nil
}

// Rename moves src to dest (creating dest parents).
func (o *FS) Rename(src, dest string) error {
	if err := errors.Join(o.guardMappingRoot(src), o.guardMappingRoot(dest)); err != nil {
		return err
	}
	source, err := o.route(src, true)
	if err != nil {
		return err
	}
	destination, err := o.route(dest, true)
	if err != nil {
		return err
	}
	if source != destination {
		return fmt.Errorf("bedfs: rename crosses path mappings")
	}
	o = source
	s, err := o.Resolve(src)
	if err != nil {
		return err
	}
	d, err := o.Resolve(dest)
	if err != nil {
		return err
	}
	if err := o.mkdirAllOwned(filepath.Dir(d)); err != nil {
		return err
	}
	sRel, err := o.relative(s)
	if err != nil {
		return err
	}
	dRel, err := o.relative(d)
	if err != nil {
		return err
	}
	return o.root.Rename(sRel, dRel)
}

// Chmod applies permissions without crossing the Bed's assigned identity.
func (o *FS) Chmod(p string, perm Permission) error {
	if err := o.ValidatePermission(p, perm); err != nil {
		return err
	}
	o, routeErr := o.route(p, true)
	if routeErr != nil {
		return routeErr
	}
	full, err := o.Resolve(p)
	if err != nil {
		return err
	}
	rel, err := o.relative(full)
	if err != nil {
		return err
	}
	f, err := o.root.Open(rel)
	if err != nil {
		return err
	}
	defer f.Close()
	if perm.Owner != "" || perm.Group != "" {
		info, err := f.Stat()
		if err != nil {
			return err
		}
		uid, gid, ok := ownerOf(info)
		if !ok {
			return fmt.Errorf("bedfs: ownership unavailable")
		}
		owner := strconv.Itoa(uid)
		group := strconv.Itoa(gid)
		if u, err := user.LookupId(owner); err == nil && perm.Owner == u.Username {
			owner = u.Username
		}
		if g, err := user.LookupGroupId(group); err == nil && perm.Group == g.Name {
			group = g.Name
		}
		if (perm.Owner != "" && perm.Owner != owner) || (perm.Group != "" && perm.Group != group) {
			return fmt.Errorf("bedfs: changing file ownership is not supported")
		}
	}
	return o.root.Chmod(rel, os.FileMode(perm.Mode)&os.ModePerm)
}

// Replace substitutes all occurrences of old with new in one file.
func (o *FS) Replace(p string, item ReplaceItem) (ReplaceResult, error) {
	o, routeErr := o.route(p, true)
	if routeErr != nil {
		return ReplaceResult{}, routeErr
	}
	full, err := o.Resolve(p)
	if err != nil {
		return ReplaceResult{}, err
	}
	rel, err := o.relative(full)
	if err != nil {
		return ReplaceResult{}, err
	}
	data, err := o.root.ReadFile(rel)
	if err != nil {
		return ReplaceResult{}, err
	}
	if item.Old == "" {
		return ReplaceResult{}, fmt.Errorf("bedfs: empty 'old' for %q", p)
	}
	count := strings.Count(string(data), item.Old)
	if count == 0 {
		return ReplaceResult{ReplacedCount: 0}, nil
	}
	li, _ := o.root.Stat(rel)
	mode := os.FileMode(0o644)
	if li != nil {
		mode = li.Mode().Perm()
	}
	out := strings.ReplaceAll(string(data), item.Old, item.New)
	if err := o.root.WriteFile(rel, []byte(out), mode); err != nil {
		return ReplaceResult{}, err
	}
	return ReplaceResult{ReplacedCount: count}, nil
}

// MakeDir creates a directory (and parents).
func (o *FS) MakeDir(p string) error {
	o, routeErr := o.route(p, true)
	if routeErr != nil {
		return routeErr
	}
	full, err := o.Resolve(p)
	if err != nil {
		return err
	}
	return o.mkdirAllOwned(full)
}

// RemoveDir removes a directory tree.
func (o *FS) RemoveDir(p string) error {
	if err := o.guardMappingRoot(p); err != nil {
		return err
	}
	o, routeErr := o.route(p, true)
	if routeErr != nil {
		return routeErr
	}
	full, err := o.Resolve(p)
	if err != nil {
		return err
	}
	rel, err := o.relative(full)
	if err != nil {
		return err
	}
	li, err := o.root.Lstat(rel)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !li.IsDir() {
		return fmt.Errorf("bedfs: %q is not a directory", p)
	}
	return o.root.RemoveAll(rel)
}

// List returns entries of a directory down to depth levels (1 = immediate).
func (o *FS) List(p string, depth int) ([]FileInfo, error) {
	if depth < 1 {
		depth = 1
	}
	var out []FileInfo
	var walk func(string, int) error
	walk = func(dir string, remaining int) error {
		entries, err := o.listDirectory(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			out = append(out, entry)
			if entry.Type == "directory" && remaining > 1 {
				if err := walk(entry.Path, remaining-1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(p, depth); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Every descent re-enters the logical view: nested mounts and common software
// cannot be bypassed by listing their parent's backing directory.
func (o *FS) listDirectory(p string) ([]FileInfo, error) {
	target, err := o.route(p, false)
	if err != nil {
		return nil, err
	}
	full, err := target.Resolve(p)
	if err != nil {
		return nil, err
	}
	rel, err := target.relative(full)
	if err != nil {
		return nil, err
	}
	info, err := target.root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("bedfs: %q is not a directory", p)
	}
	dir, err := target.root.Open(rel)
	if err != nil {
		return nil, err
	}
	entries, readErr := dir.ReadDir(-1)
	if err := errors.Join(readErr, dir.Close()); err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, entry := range entries {
		names[entry.Name()] = true
	}
	client := o.clientPath(p)
	for _, mapping := range o.mappings {
		if path.Dir(mapping.mapping.Target) == client {
			names[path.Base(mapping.mapping.Target)] = true
		}
	}
	for _, shared := range o.shared {
		if path.Dir(shared.paths.prefix) == client {
			names[path.Base(shared.paths.prefix)] = true
		}
	}
	var out []FileInfo
	for name := range names {
		item, err := o.Stat(path.Join(client, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// searchLimit bounds results for large shared software trees as well as workspaces.
const searchLimit = 1000

func (o *FS) Search(p, pattern string) ([]FileInfo, error) {
	var out []FileInfo
	var walk func(string) error
	walk = func(dir string) error {
		entries, err := o.listDirectory(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if len(out) >= searchLimit {
				return nil
			}
			if entry.Type == "directory" {
				if err := walk(entry.Path); err != nil {
					return err
				}
				continue
			}
			name := path.Base(entry.Path)
			matches := strings.Contains(name, pattern)
			if strings.ContainsAny(pattern, "*?[") {
				matches, _ = path.Match(pattern, name)
			}
			if matches {
				out = append(out, entry)
			}
		}
		return nil
	}
	if err := walk(p); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (o *FS) ClientWorkdir() string { return o.paths.clientWorkdir() }

// SetWorkdir fixes the Pillow-relative path base before publishing this view.
func (o *FS) SetWorkdir(p string) {
	o.paths.workdir = p
	for _, m := range o.mappings {
		m.fs.paths.workdir = p
	}
	for _, shared := range o.shared {
		shared.paths.workdir = p
	}
}
