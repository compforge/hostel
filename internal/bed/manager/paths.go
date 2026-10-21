package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"

	model "github.com/qiankunli/hostel/internal/bed"
)

var ErrPathsConflict = errors.New("bed: path declaration cannot change")

func checkBedPaths(options CreateOptions, actual model.Spec) error {
	if options.lookup {
		return nil
	}
	if options.Volumes != nil && !reflect.DeepEqual(options.Volumes, actual.Volumes) {
		return fmt.Errorf("%w: volumes", ErrPathsConflict)
	}
	if options.SyncPaths != nil && !slices.Equal(options.SyncPaths, actual.SyncPaths) {
		return fmt.Errorf("%w: sync_paths", ErrPathsConflict)
	}
	return nil
}

// Stage-in replaces the working tree. Resolve its portable data contract before
// preparing file handles or starting any process that could alter restored data.
func (m *Manager) preparePaths(init *bedInitialization) error {
	b := init.model
	spec := b.Spec()
	var meta bedMeta
	data, readErr := os.ReadFile(metaPath(spec.Dir))
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	paths := spec.SyncPaths
	if readErr == nil {
		// A corrupt data contract must not silently revert to /workspace and drop
		// previously synced roots from the next checkpoint.
		if err := json.Unmarshal(data, &meta); err != nil {
			return fmt.Errorf("bed: read path contract: %w", err)
		}
		restored := meta.SyncPaths
		if restored == nil {
			restored = []string{"/workspace"}
		}
		restored, err := model.NormalizeSyncPaths(restored)
		if err != nil {
			return err
		}
		if paths != nil && !slices.Equal(paths, restored) {
			return fmt.Errorf("%w: snapshot sync_paths", ErrPathsConflict)
		}
		paths = restored
	}
	if paths == nil {
		paths = []string{"/workspace"}
	}
	paths, err := model.NormalizeSyncPaths(paths)
	if err != nil {
		return err
	}
	model.SpecWriter{}.Update(b, func(s *model.Spec) { s.SyncPaths = paths })
	if err := model.ValidateVolumeMounts(b.Spec()); err != nil {
		return err
	}
	return m.saveLocalIdentity(init.local)
}
