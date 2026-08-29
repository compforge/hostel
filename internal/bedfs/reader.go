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
	"fmt"
	"os"
	"path"
)

// Reader is the read-only projection of one BedFS. fallbackRoot is normally
// empty; an exclusive dorm deployment may explicitly supply the Executor's
// process root because it shares the carrier mount namespace.
//
// +spec=`Reads prefer the BedFS path. When that path is absent, an absolute client path may be retried below the explicitly configured fallback root; relative paths never fall back.`
// +why=`Dorm commands can successfully write a literal absolute path in the shared process namespace, while structured file paths still map into bed_home. Opt-in read-only recovery preserves those artifacts without weakening mutation confinement.`
type Reader struct {
	primary      *FS
	fallbackRoot string
}

// NewReader creates a read projection. An empty fallbackRoot keeps all reads
// strictly inside primary. A non-empty root exposes that process view through
// read APIs and is therefore only safe for an explicitly exclusive carrier.
func NewReader(primary *FS, fallbackRoot string) *Reader {
	return &Reader{primary: primary, fallbackRoot: fallbackRoot}
}

func readWithFallback[T any](r *Reader, clientPath string, read func(*FS) (T, error)) (T, error) {
	value, err := read(r.primary)
	if err == nil || r.fallbackRoot == "" || !path.IsAbs(clientPath) || !os.IsNotExist(err) {
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

func (r *Reader) List(p string, depth int) ([]FileInfo, error) {
	return readWithFallback(r, p, func(fs *FS) ([]FileInfo, error) { return fs.List(p, depth) })
}

func (r *Reader) Search(p, pattern string) ([]FileInfo, error) {
	return readWithFallback(r, p, func(fs *FS) ([]FileInfo, error) { return fs.Search(p, pattern) })
}
