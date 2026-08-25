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

package store

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// tarStore is the full-snapshot S3 backend. Each Persist builds one complete
// tar.gz in a temporary file and atomically replaces the bed's single object.
// It minimizes remote object count and implementation complexity at the cost
// of uploading and downloading the entire workspace after every change.
//
// Layout:
//
//	<prefix>/tar/<bedID>/snapshot.tar.gz
type tarStore struct {
	obj    objAPI
	prefix string
}

// A tar object can be much larger than one CAS chunk or pack. Keep its object
// operation bounded, but allow enough time for a full workspace on a slow link.
const tarOpTimeout = 30 * time.Minute

func newTar(ctx context.Context, cfg Config) (Store, error) {
	obj, err := newS3Obj(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return newTarStore(obj, cfg.Prefix), nil
}

func newTarStore(obj objAPI, prefix string) *tarStore { return &tarStore{obj: obj, prefix: prefix} }

func (s *tarStore) Name() string { return "tar" }

func (s *tarStore) bedPrefix(bedID string) string {
	return path.Join(s.prefix, "tar", bedID) + "/"
}

func (s *tarStore) snapshotKey(bedID string) string {
	return s.bedPrefix(bedID) + "snapshot.tar.gz"
}

func (s *tarStore) Stat(ctx context.Context, bedID string) (*SnapshotInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, s3OpTimeout)
	defer cancel()
	meta, size, exists, err := s.obj.head(ctx, s.snapshotKey(bedID))
	if err != nil || !exists {
		return nil, err
	}
	info := &SnapshotInfo{Bytes: size}
	if g, err := strconv.ParseInt(meta[generationMetaKey], 10, 64); err == nil {
		info.Generation = g
	}
	return info, nil
}

func (s *tarStore) Persist(ctx context.Context, bedID, dir string, generation int64) error {
	meta, _, exists, err := s.obj.head(ctx, s.snapshotKey(bedID))
	if err != nil {
		return fmt.Errorf("store: persist %s: pre-write stat: %w", bedID, err)
	}
	if exists {
		if g, err := strconv.ParseInt(meta[generationMetaKey], 10, 64); err == nil && g >= generation {
			return fmt.Errorf("store: persist %s: remote generation %d >= local %d: %w",
				bedID, g, generation, ErrConflict)
		}
	}

	tmp, err := os.CreateTemp("", "hostel-snapshot-*.tar.gz")
	if err != nil {
		return fmt.Errorf("store: persist %s: create temporary archive: %w", bedID, err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	if err := writeTarGzip(dir, tmp); err != nil {
		return fmt.Errorf("store: persist %s: archive: %w", bedID, err)
	}
	size, err := tmp.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("store: persist %s: size archive: %w", bedID, err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("store: persist %s: rewind archive: %w", bedID, err)
	}

	putCtx, cancel := context.WithTimeout(ctx, tarOpTimeout)
	defer cancel()
	if err := s.obj.put(putCtx, s.snapshotKey(bedID), tmp, size, map[string]string{
		generationMetaKey: strconv.FormatInt(generation, 10),
	}); err != nil {
		return fmt.Errorf("store: persist %s: put archive: %w", bedID, err)
	}
	return nil
}

func (s *tarStore) Restore(ctx context.Context, bedID, dir string) error {
	getCtx, cancel := context.WithTimeout(ctx, tarOpTimeout)
	defer cancel()
	rc, err := s.obj.get(getCtx, s.snapshotKey(bedID))
	if err != nil {
		return fmt.Errorf("store: restore %s: get archive: %w", bedID, err)
	}
	defer rc.Close()
	if err := readTarGzip(rc, dir); err != nil {
		return fmt.Errorf("store: restore %s: extract archive: %w", bedID, err)
	}
	return nil
}

func (s *tarStore) Delete(ctx context.Context, bedID string) error {
	ctx, cancel := context.WithTimeout(ctx, s3OpTimeout)
	defer cancel()
	if err := s.obj.del(ctx, []string{s.snapshotKey(bedID)}); err != nil {
		return fmt.Errorf("store: delete %s: %w", bedID, err)
	}
	return nil
}

func writeTarGzip(root string, dst io.Writer) error {
	gz := gzip.NewWriter(dst)
	tw := tar.NewWriter(gz)
	walkErr := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if snapshotPathExcluded(rel) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		if !mode.IsRegular() && !mode.IsDir() && mode&os.ModeSymlink == 0 {
			return fmt.Errorf("unsupported file type %s at %s", mode.Type(), rel)
		}
		var link string
		if mode&os.ModeSymlink != 0 {
			link, err = os.Readlink(name)
			if err != nil {
				return err
			}
			link = filepath.ToSlash(link)
			if err := validateTarSymlink(filepath.ToSlash(rel), link); err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if !mode.IsRegular() {
			return nil
		}

		file, err := os.Open(name)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, file)
		closeErr := file.Close()
		return errors.Join(copyErr, closeErr)
	})
	tarErr := tw.Close()
	gzipErr := gz.Close()
	return errors.Join(walkErr, tarErr, gzipErr)
}

type restoredDir struct {
	name    string
	mode    os.FileMode
	modTime time.Time
}

func readTarGzip(src io.Reader, dir string) error {
	gz, err := gzip.NewReader(src)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		gz.Close()
		return err
	}
	defer root.Close()

	tr := tar.NewReader(gz)
	seen := make(map[string]struct{})
	var dirs []restoredDir
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			gz.Close()
			return err
		}
		name, err := cleanTarPath(header.Name)
		if err != nil {
			gz.Close()
			return err
		}
		if _, ok := seen[name]; ok {
			gz.Close()
			return fmt.Errorf("duplicate archive path %q", name)
		}
		seen[name] = struct{}{}
		// A durable workspace snapshot may restore on a different carrier and
		// must not grant setuid/setgid or ownership-derived privilege there.
		mode := os.FileMode(header.Mode).Perm()

		switch header.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(filepath.FromSlash(name), mode.Perm()|0o700); err != nil {
				gz.Close()
				return err
			}
			dirs = append(dirs, restoredDir{name: filepath.FromSlash(name), mode: mode, modTime: header.ModTime})
		case tar.TypeReg, tar.TypeRegA:
			if err := root.MkdirAll(filepath.Dir(filepath.FromSlash(name)), 0o755); err != nil {
				gz.Close()
				return err
			}
			file, err := root.OpenFile(filepath.FromSlash(name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
			if err != nil {
				gz.Close()
				return err
			}
			_, copyErr := io.CopyN(file, tr, header.Size)
			closeErr := file.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				gz.Close()
				return err
			}
			if err := root.Chmod(filepath.FromSlash(name), mode.Perm()); err != nil {
				gz.Close()
				return err
			}
			if !header.ModTime.IsZero() {
				if err := root.Chtimes(filepath.FromSlash(name), header.ModTime, header.ModTime); err != nil {
					gz.Close()
					return err
				}
			}
		case tar.TypeSymlink:
			if err := validateTarSymlink(name, header.Linkname); err != nil {
				gz.Close()
				return err
			}
			if err := root.MkdirAll(filepath.Dir(filepath.FromSlash(name)), 0o755); err != nil {
				gz.Close()
				return err
			}
			if err := root.Symlink(filepath.FromSlash(header.Linkname), filepath.FromSlash(name)); err != nil {
				gz.Close()
				return err
			}
		default:
			gz.Close()
			return fmt.Errorf("unsupported archive entry type %d at %q", header.Typeflag, name)
		}
	}

	// Directories stay owner-writable while children are created; restore their
	// actual modes and mtimes from leaves to root only after extraction finishes.
	for i := len(dirs) - 1; i >= 0; i-- {
		d := dirs[i]
		if err := root.Chmod(d.name, d.mode.Perm()); err != nil {
			gz.Close()
			return err
		}
		if !d.modTime.IsZero() {
			if err := root.Chtimes(d.name, d.modTime, d.modTime); err != nil {
				gz.Close()
				return err
			}
		}
	}
	// tar.Reader stops at the tar end marker. Drain gzip so its CRC and size
	// trailer are checked before a restored workspace is accepted.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		gz.Close()
		return err
	}
	return gz.Close()
}

func cleanTarPath(name string) (string, error) {
	if strings.Contains(name, `\`) {
		return "", fmt.Errorf("archive path %q contains a backslash", name)
	}
	clean := path.Clean(name)
	if clean == "." || !fs.ValidPath(clean) {
		return "", fmt.Errorf("archive path %q escapes the restore root", name)
	}
	return clean, nil
}

func validateTarSymlink(name, target string) error {
	if target == "" || path.IsAbs(target) || strings.Contains(target, `\`) {
		return fmt.Errorf("symlink %q has unsafe target %q", name, target)
	}
	resolved := path.Clean(path.Join(path.Dir(name), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("symlink %q target %q escapes the restore root", name, target)
	}
	return nil
}
