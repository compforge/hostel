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

package bedfs

import (
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path"
	"sort"
	"strings"
)

// Reader resolves reads using the selected process view. Unredirected mappings
// try Bed-local data before their declared source. fallbackRoot is normally
// empty; an exclusive dorm deployment may explicitly supply the Executor's
// process root because it shares the carrier mount namespace.
//
// +spec=`Unredirected mapping reads try Bed-local data, then the declared HostPath; return the first successful read and keep both attempts within their anchored roots. Other reads prefer the BedFS path. When that path is absent, an absolute client path may be retried below the explicitly configured fallback root; relative paths never fall back.`
// +why=`Shared commands can successfully write a literal absolute path in the shared process namespace, while structured file paths still map into bed_home. Opt-in read-only recovery preserves those artifacts without weakening mutation confinement.`
type Reader struct {
	primary      *FS
	view         ProcessView
	fallbackRoot string
}

// NewReader creates a read projection. An empty fallbackRoot keeps all reads
// inside the Bed data root and its declared sources. A non-empty root exposes that process view through
// read APIs and is therefore only safe for an explicitly exclusive carrier.
func NewReader(view ProcessView, fallbackRoot string) *Reader {
	return &Reader{primary: view.fs, view: view, fallbackRoot: fallbackRoot}
}

func readWithFallback[T any](r *Reader, clientPath string, read func(*FS) (T, error)) (T, error) {
	if mapped := r.primary.mapped(clientPath); mapped != nil && !r.view.MappingSupport().Supports(mapped.readOnly) {
		// A borrowed read-only view skips the mapping router, so the first
		// candidate belongs to this Bed's data root, never daemon-global /.
		local := &FS{root: r.primary.root, paths: r.primary.paths, uid: -1, gid: -1}
		placeholder, _ := emptyMappingPlaceholder(local, mapped.paths.prefix, clientPath)
		if !placeholder {
			value, err := read(local)
			if err == nil {
				return value, nil
			}
		}
		// A failed local read may still succeed at the separately authorized source.
		// Only the declaration's anchored HostPath is eligible. In particular,
		// mapped reads never proceed into the exclusive-carrier fallback.
		return read(mapped)
	}
	value, err := read(r.primary)
	if r.primary.mapped(clientPath) != nil || err == nil || r.fallbackRoot == "" || !path.IsAbs(clientPath) || !os.IsNotExist(err) {
		return value, err
	}

	fallback, openErr := New(r.fallbackRoot)
	if openErr != nil {
		var zero T
		return zero, fmt.Errorf("bedfs: open absolute read fallback %q: %w", r.fallbackRoot, openErr)
	}
	defer fallback.Close()
	return read(fallback)
}

func (r *Reader) Stat(p string) (FileInfo, error) {
	return readWithFallback(r, p, func(fs *FS) (FileInfo, error) { return fs.Stat(p) })
}

func (r *Reader) Read(p string) ([]byte, error) {
	return readWithFallback(r, p, func(fs *FS) ([]byte, error) { return fs.Read(p) })
}

func (r *Reader) ReadLines(p string, offset, limit int) (string, error) {
	return readWithFallback(r, p, func(fs *FS) (string, error) { return fs.ReadLines(p, offset, limit) })
}

// walk reselects candidates at every directory, so parent list/search requests
// obey the same Bed-local precedence as direct reads of a mapped path.
func (r *Reader) walk(p string, depth int, visit func(FileInfo) error) error {
	entries, err := readWithFallback(r, p, func(fs *FS) ([]FileInfo, error) { return fs.List(p, 1) })
	if err != nil {
		return err
	}
	for _, entry := range entries {
		entry, err = r.Stat(entry.Path)
		if err != nil {
			return err
		}
		if err := visit(entry); err != nil {
			return err
		}
		if entry.Type == "directory" && depth != 1 {
			if err := r.walk(entry.Path, depth-1, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Reader) List(p string, depth int) ([]FileInfo, error) {
	if depth < 1 {
		depth = 1
	}
	var entries []FileInfo
	err := r.walk(p, depth, func(entry FileInfo) error { entries = append(entries, entry); return nil })
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func (r *Reader) Search(p, pattern string) ([]FileInfo, error) {
	var entries []FileInfo
	err := r.walk(p, 0, func(entry FileInfo) error {
		if entry.Type == "directory" {
			return nil
		}
		name := path.Base(entry.Path)
		matches := strings.Contains(name, pattern)
		if strings.ContainsAny(pattern, "*?[") {
			matches, _ = path.Match(pattern, name)
		}
		if matches {
			entries = append(entries, entry)
		}
		if len(entries) >= searchLimit {
			return iofs.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, iofs.SkipAll) {
		return nil, err
	}
	return entries, nil
}

// SetPathMappings creates an empty local directory for parent discovery. It
// must not make a source directory look empty when the process mapping is absent.
func emptyMappingPlaceholder(local *FS, prefix, p string) (bool, error) {
	if clientPath(p) != prefix {
		return false, nil
	}
	host, err := local.Resolve(p)
	if err != nil {
		return false, err
	}
	rel, err := local.relative(host)
	if err != nil {
		return false, err
	}
	info, err := local.root.Lstat(rel)
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, nil
	}
	dir, err := local.root.Open(rel)
	if err != nil {
		return false, err
	}
	defer dir.Close()
	_, err = dir.ReadDir(1)
	if errors.Is(err, io.EOF) {
		return true, nil
	}
	return false, err
}
