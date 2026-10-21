package manager

import (
	"errors"
	"fmt"
	"reflect"

	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/pillow"
	"github.com/qiankunli/hostel/internal/bed/store"
)

var ErrPathsConflict = errors.New("bed: volume declaration cannot change")

func checkBedPaths(options CreateOptions, actual model.Spec) error {
	if options.Volumes != nil && !reflect.DeepEqual(options.Volumes, actual.Volumes) {
		return fmt.Errorf("%w: volumes", ErrPathsConflict)
	}
	return nil
}

// Resolve the portable Volume contract before opening file handles or starting
// writers. A requested declaration cannot silently drop restored durable data.
func (m *Manager) preparePaths(init *bedInitialization) error {
	b := init.model
	spec := b.Spec()
	restored, exists, err := store.ReadVolumeContract(spec.Dir)
	if err != nil {
		return err
	}
	if exists {
		if spec.Volumes == nil {
			spec.Volumes = restored
		} else if !reflect.DeepEqual(model.ManagedVolumes(spec.Volumes), restored) {
			return fmt.Errorf("%w: snapshot managed volumes", ErrPathsConflict)
		}
	}
	if spec.Volumes == nil {
		policy := "sync"
		if spec.StoreKind == model.SyncNoop {
			policy = "none"
		}
		spec.Volumes = []model.VolumeSpec{{Name: "workspace", Managed: &model.ManagedVolume{Persistence: policy}}}
	}
	spec.Volumes, err = model.NormalizeVolumes(spec.Volumes)
	if err != nil {
		return err
	}
	if len(model.PersistentVolumeRoots(spec.Volumes)) > 0 && spec.StoreKind == model.SyncNoop {
		return fmt.Errorf("managed persistence=sync requires a configured durable Store")
	}
	spec.Pillows, err = pillow.Normalize(spec.Pillows)
	if err != nil {
		return err
	}
	if err := model.ValidateVolumeMounts(spec); err != nil {
		return err
	}
	model.SpecWriter{}.Update(b, func(s *model.Spec) { *s = spec })
	return m.saveLocalIdentity(init.local)
}
