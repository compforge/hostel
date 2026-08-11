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
// Path contract: the client's "/" is bed_home. Absolute paths are rebased
// below it, /workspace names a real subdirectory, and relative paths are
// workspace-relative. A Bed never sees carrier paths through the API.
package bedfs

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FileInfo mirrors the OpenSandbox execd file metadata shape so existing SDKs
// deserialize hostel responses unchanged. Paths are reported back in client
// form (bed-rooted; symmetric with what the caller sent).
type FileInfo struct {
	Path       string    `json:"path,omitempty"`
	Type       string    `json:"type,omitempty"` // "file" | "directory" | "symlink"
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at,omitzero"`
	Owner      string    `json:"owner"`
	Group      string    `json:"group"`
	Mode       int       `json:"mode"`
}

// ReplaceItem / ReplaceResult mirror execd's /files/replace shapes.
type ReplaceItem struct {
	Old string `json:"old,omitempty"`
	New string `json:"new,omitempty"`
}
type ReplaceResult struct {
	ReplacedCount int `json:"replacedCount"`
}

// RenameItem mirrors execd's /files/mv item shape.
type RenameItem struct {
	Src  string `json:"src,omitempty"`
	Dest string `json:"dest,omitempty"`
}

// Permission mirrors execd's permission shape (owner/group accepted but not
// applied in v1 — hostel runs beds under one uid; only mode is applied).
type Permission struct {
	Owner string `json:"owner"`
	Group string `json:"group"`
	Mode  int    `json:"mode"`
}

// FS is one bed's filesystem. It is created with the Bed and survives
// Executor replacement; all daemon file operations are confined to it.
type FS struct {
	paths paths
	root  *os.Root
	// uid/gid of the workspace dir when it differs from the daemon's euid
	// (uid-isolated beds), else -1. Mechanism-independent invariant: whatever
	// lands in a bed's workspace belongs to the bed — BedFS runs as the
	// daemon, so without this, file-API writes would leave daemon-owned files
	// the bed can read but not modify (docs/data.md, room/uid).
	uid, gid int
}

// New returns file ops confined to home (the bed_home host directory).
// File ops never need the mount view, so the embedded Paths carries no mount
// point; exec-side callers use the bed's own Paths for that.
func New(root string) (*FS, error) {
	confined, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("bedfs: open root %q: %w", root, err)
	}
	o := &FS{paths: newPaths(root), root: confined, uid: -1, gid: -1}
	if fi, err := os.Lstat(root); err == nil {
		if uid, gid, ok := ownerOf(fi); ok && uid != os.Geteuid() {
			o.uid, o.gid = uid, gid
		}
	}
	return o, nil
}

// Close releases the descriptor anchoring this BedFS. A Bed keeps it open for
// its resident lifetime so renames and symlink swaps cannot redirect daemon
// file operations outside bed_home.
func (o *FS) Close() error { return o.root.Close() }

func (o *FS) relative(full string) (string, error) {
	rel, err := filepath.Rel(o.Home(), full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("bedfs: carrier path %q is outside bed_home %q", full, o.Home())
	}
	return rel, nil
}

// chownNew hands a path BedFS just created over to the workspace owner.
// Best-effort: if the daemon lacks CAP_CHOWN the file stays daemon-owned,
// which is the pre-invariant behavior (bed reads it, next Prepare re-chowns).
// Same hardlink discipline as isolation's chownTree: a multiply-linked file is
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
	if err := o.root.MkdirAll(rel, 0o755); err != nil {
		return err
	}
	if o.uid < 0 {
		return nil
	}
	for d := dir; strings.HasPrefix(d, o.Home()); d = filepath.Dir(d) {
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
	return o.mkdirAllOwned(dir)
}

// Resolve maps a client path to a carrier path under bed_home. It is shared by
// file operations and structured Executor paths such as cwd.
func (o *FS) Resolve(p string) (string, error) { return o.paths.FromClient(p) }

// Home is the carrier path of bed_home, which is the client's "/".
func (o *FS) Home() string { return o.paths.Home() }

// Workspace is the carrier path of the Bed's default workspace.
func (o *FS) Workspace() string { return o.paths.WorkspaceHost() }

func (o *FS) virtual(full string) string { return o.paths.ToClient(full) }

func (o *FS) info(full string, li os.FileInfo) FileInfo {
	typ := "file"
	switch {
	case li.IsDir():
		typ = "directory"
	case li.Mode()&os.ModeSymlink != 0:
		typ = "symlink"
	}
	return FileInfo{
		Path:       o.virtual(full),
		Type:       typ,
		Size:       li.Size(),
		ModifiedAt: li.ModTime(),
		Mode:       int(li.Mode().Perm()),
	}
}

// Stat returns metadata for one path.
func (o *FS) Stat(p string) (FileInfo, error) {
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
	full, err := o.Resolve(p)
	if err != nil {
		return err
	}
	if err := o.mkdirAllOwned(filepath.Dir(full)); err != nil {
		return err
	}
	fm := os.FileMode(0o644)
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

// Chmod applies mode bits. Owner/group are accepted for spec compatibility but
// not applied in v1 (single-uid beds); real setuid lands with the OSEP-0013
// isolation port.
func (o *FS) Chmod(p string, perm Permission) error {
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
	return f.Chmod(os.FileMode(perm.Mode) & os.ModePerm)
}

// Replace substitutes all occurrences of old with new in one file.
func (o *FS) Replace(p string, item ReplaceItem) (ReplaceResult, error) {
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
	full, err := o.Resolve(p)
	if err != nil {
		return err
	}
	return o.mkdirAllOwned(full)
}

// RemoveDir removes a directory tree.
func (o *FS) RemoveDir(p string) error {
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
	full, err := o.Resolve(p)
	if err != nil {
		return nil, err
	}
	rel, err := o.relative(full)
	if err != nil {
		return nil, err
	}
	li, err := o.root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if li.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("bedfs: %q is a symlink, refusing to traverse", p)
	}
	if !li.IsDir() {
		return nil, fmt.Errorf("bedfs: %q is not a directory", p)
	}
	if depth < 1 {
		depth = 1
	}
	var out []FileInfo
	var walk func(dir, rel string, d int) error
	walk = func(dir, rel string, d int) error {
		opened, err := o.root.Open(rel)
		if err != nil {
			return err
		}
		entries, err := opened.ReadDir(-1)
		closeErr := opened.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		for _, e := range entries {
			fi, err := e.Info()
			if err != nil {
				continue
			}
			out = append(out, o.info(filepath.Join(dir, e.Name()), fi))
			if e.IsDir() && d > 1 {
				if err := walk(filepath.Join(dir, e.Name()), filepath.Join(rel, e.Name()), d-1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(full, rel, depth); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// searchLimit bounds Search results so a huge workspace can't OOM the daemon.
const searchLimit = 1000

// Search walks under p and returns files whose base name matches pattern
// (glob when pattern contains meta characters, substring otherwise).
func (o *FS) Search(p, pattern string) ([]FileInfo, error) {
	full, err := o.Resolve(p)
	if err != nil {
		return nil, err
	}
	match := func(name string) bool { return true }
	if pattern != "" {
		if strings.ContainsAny(pattern, "*?[") {
			match = func(name string) bool {
				ok, _ := path.Match(pattern, name)
				return ok
			}
		} else {
			match = func(name string) bool { return strings.Contains(name, pattern) }
		}
	}
	rel, err := o.relative(full)
	if err != nil {
		return nil, err
	}
	var out []FileInfo
	var walk func(dir, rel string) error
	walk = func(dir, rel string) error {
		opened, err := o.root.Open(rel)
		if err != nil {
			return err
		}
		entries, err := opened.ReadDir(-1)
		closeErr := opened.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		for _, entry := range entries {
			if len(out) >= searchLimit {
				return nil
			}
			entryPath := filepath.Join(dir, entry.Name())
			entryRel := filepath.Join(rel, entry.Name())
			if entry.IsDir() {
				if err := walk(entryPath, entryRel); err != nil {
					return err
				}
				continue
			}
			if !match(entry.Name()) {
				continue
			}
			fi, err := entry.Info()
			if err == nil {
				out = append(out, o.info(entryPath, fi))
			}
		}
		return nil
	}
	if err := walk(full, rel); err != nil {
		return nil, err
	}
	return out, nil
}
