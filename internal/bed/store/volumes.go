package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/qiankunli/hostel/internal/bed"
)

// ReadVolumeContract reads the persistent-volume declarations in a snapshot.
func ReadVolumeContract(dir string) ([]bed.VolumeSpec, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var manifest struct {
		Version int              `json:"version"`
		Volumes []bed.VolumeSpec `json:"volumes"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, false, err
	}
	if manifest.Version != 2 || manifest.Volumes == nil {
		return nil, false, fmt.Errorf("invalid managed volume manifest")
	}
	volumes, err := bed.NormalizeVolumes(manifest.Volumes)
	if err != nil {
		return nil, false, err
	}
	for _, v := range volumes {
		if v.Managed == nil || v.Managed.Persistence != "sync" {
			return nil, false, fmt.Errorf("snapshot contains non-persistent volume %s", v.Name)
		}
	}
	return volumes, true, nil
}

func validateRestoredVolumes(dir string, requested []bed.VolumeSpec) error {
	volumes, exists, err := ReadVolumeContract(dir)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("snapshot has no managed volume manifest")
	}
	if requested != nil && !reflect.DeepEqual(bed.PersistentVolumes(requested), volumes) {
		return fmt.Errorf("snapshot persistent volumes conflict with requested declaration")
	}
	for _, v := range volumes {
		info, err := os.Lstat(filepath.Join(dir, "volumes", v.Name))
		if err != nil {
			return fmt.Errorf("restore volume %s: %w", v.Name, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("restore volume %s is not a directory", v.Name)
		}
	}
	return nil
}
