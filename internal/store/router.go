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
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
)

// New selects an explicit backend or builds the per-bed auto router. Explicit
// backends never inspect or migrate another layout. Auto with no bucket remains
// noop; with a bucket it recognizes each bed's committed layout before routing.
func New(ctx context.Context, cfg Config) (Store, error) {
	switch cfg.Backend {
	case "", "auto":
		if cfg.Bucket == "" {
			return Noop{}, nil
		}
		if cfg.AutoPackFileThreshold < 0 {
			return nil, fmt.Errorf("store: auto pack file threshold must be non-negative")
		}
		return newAuto(ctx, cfg)
	case "noop":
		return Noop{}, nil
	case "s3", "cas":
		if cfg.Bucket == "" {
			return nil, fmt.Errorf("store: s3 backend requires a bucket")
		}
		return newCAS(ctx, cfg)
	case "pack":
		if cfg.Bucket == "" {
			return nil, fmt.Errorf("store: pack backend requires a bucket")
		}
		return newPack(ctx, cfg)
	case "tar":
		if cfg.Bucket == "" {
			return nil, fmt.Errorf("store: tar backend requires a bucket")
		}
		return newTar(ctx, cfg)
	default:
		return nil, fmt.Errorf("store: unknown backend %q", cfg.Backend)
	}
}

type storeLayout string

const (
	layoutCAS  storeLayout = "cas"
	layoutPack storeLayout = "pack"
	layoutTar  storeLayout = "tar"
)

type routedBackend struct {
	layout storeLayout
	store  Store
}

type inspectedBackend struct {
	routedBackend
	info *SnapshotInfo
	err  error
}

type autoState struct {
	selected *inspectedBackend
	byLayout map[storeLayout]inspectedBackend
}

// autoStore owns selection and transition policy, not a persistence format. It
// delegates every data operation to CAS, pack or tar. New beds use pack; CAS
// exists for backward compatibility. CAS -> pack publishes a newer pack
// snapshot but deliberately leaves the old CAS objects untouched; only
// explicit purge removes durable objects.
type autoStore struct {
	cas               *casStore
	pack              *packStore
	tar               *tarStore
	packFileThreshold int
	filter            snapshotFilter
}

func newAuto(ctx context.Context, cfg Config) (Store, error) {
	obj, err := newS3Obj(ctx, cfg)
	if err != nil {
		return nil, err
	}
	filter, err := newSnapshotFilter(cfg.PersistedPaths)
	if err != nil {
		return nil, err
	}
	return newAutoStore(obj, cfg.Prefix, cfg.AutoPackFileThreshold, filter), nil
}

func newAutoStore(obj objAPI, prefix string, packFileThreshold int, filters ...snapshotFilter) *autoStore {
	filter := defaultSnapshotFilter()
	if len(filters) > 0 {
		filter = filters[0]
	}
	return &autoStore{
		cas:               newCASStore(obj, prefix, filter),
		pack:              newPackStore(obj, prefix, filter),
		tar:               newTarStore(obj, prefix, filter),
		packFileThreshold: packFileThreshold,
		filter:            filter,
	}
}

func (s *autoStore) Name() string { return "auto" }

func (s *autoStore) backends() []routedBackend {
	return []routedBackend{
		{layout: layoutCAS, store: s.cas},
		{layout: layoutPack, store: s.pack},
		{layout: layoutTar, store: s.tar},
	}
}

func (s *autoStore) inspect(ctx context.Context, bedID string) (autoState, error) {
	backends := s.backends()
	results := make(chan inspectedBackend, len(backends))
	for _, backend := range backends {
		go func() {
			info, err := backend.store.Stat(ctx, bedID)
			results <- inspectedBackend{routedBackend: backend, info: info, err: err}
		}()
	}

	state := autoState{byLayout: make(map[storeLayout]inspectedBackend)}
	var inspectErr error
	for range backends {
		result := <-results
		if result.err != nil {
			inspectErr = errors.Join(inspectErr, fmt.Errorf("%s: %w", result.layout, result.err))
			continue
		}
		if result.info == nil {
			continue
		}
		state.byLayout[result.layout] = result
		if state.selected == nil || result.info.Generation > state.selected.info.Generation {
			selected := result
			state.selected = &selected
		}
	}
	if inspectErr != nil {
		return state, fmt.Errorf("store: inspect bed %s layouts: %w", bedID, inspectErr)
	}
	if state.selected == nil {
		return state, nil
	}
	for layout, result := range state.byLayout {
		if layout != state.selected.layout && result.info.Generation == state.selected.info.Generation {
			return state, fmt.Errorf(
				"store: bed %s has ambiguous %s and %s snapshots at generation %d",
				bedID, state.selected.layout, layout, result.info.Generation,
			)
		}
	}
	return state, nil
}

func (s *autoStore) Stat(ctx context.Context, bedID string) (*SnapshotInfo, error) {
	state, err := s.inspect(ctx, bedID)
	if err != nil || state.selected == nil {
		return nil, err
	}
	return state.selected.info, nil
}

func (s *autoStore) Restore(ctx context.Context, bedID, dir string) error {
	state, err := s.inspect(ctx, bedID)
	if err != nil {
		return err
	}
	if state.selected == nil {
		return fmt.Errorf("store: restore %s: snapshot not found", bedID)
	}
	return state.selected.store.Restore(ctx, bedID, dir)
}

func (s *autoStore) Persist(ctx context.Context, bedID, dir string, generation int64) error {
	state, err := s.inspect(ctx, bedID)
	if err != nil {
		return err
	}
	if state.selected != nil && state.selected.info.Generation >= generation {
		return fmt.Errorf("store: persist %s: remote %s generation %d >= local %d: %w",
			bedID, state.selected.layout, state.selected.info.Generation, generation, ErrConflict)
	}

	// New auto-routed beds use pack. CAS remains available only for existing
	// customers and switches one-way once its persisted tree crosses the
	// configured file-count threshold.
	target := routedBackend{layout: layoutPack, store: s.pack}
	if state.selected != nil {
		target = state.selected.routedBackend
	}
	if state.selected != nil && state.selected.layout == layoutCAS {
		usePack, err := exceedsSnapshotFileThreshold(dir, s.packFileThreshold, s.filter)
		if err != nil {
			return fmt.Errorf("store: persist %s: count snapshot files: %w", bedID, err)
		}
		if usePack {
			target = routedBackend{layout: layoutPack, store: s.pack}
		}
	}
	return target.store.Persist(ctx, bedID, dir, generation)
}

func (s *autoStore) Delete(ctx context.Context, bedID string) error {
	var err error
	for _, backend := range s.backends() {
		if deleteErr := backend.store.Delete(ctx, bedID); deleteErr != nil {
			err = errors.Join(err, fmt.Errorf("%s: %w", backend.layout, deleteErr))
		}
	}
	if err != nil {
		return fmt.Errorf("store: delete %s layouts: %w", bedID, err)
	}
	return nil
}

var errFileThresholdExceeded = errors.New("snapshot file threshold exceeded")

func exceedsSnapshotFileThreshold(root string, threshold int, filters ...snapshotFilter) (bool, error) {
	if threshold <= 0 {
		return false, nil
	}
	count := 0
	filter := defaultSnapshotFilter()
	if len(filters) > 0 {
		filter = filters[0]
	}
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, name)
		if err != nil || rel == "." {
			return err
		}
		if filter.excluded(rel) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		count++
		if count > threshold {
			return errFileThresholdExceeded
		}
		return nil
	})
	if errors.Is(err, errFileThresholdExceeded) {
		return true, nil
	}
	return false, err
}
