package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/qiankunli/hostel/internal/bed"
)

// ReadVolumeContract reads the portable managed-volume declaration. Environment
// configuration and HostPath sources never belong to this contract.
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
		if v.Managed == nil {
			return nil, false, fmt.Errorf("snapshot contains external volume %s", v.Name)
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
	if requested != nil && !reflect.DeepEqual(bed.ManagedVolumes(requested), volumes) {
		return fmt.Errorf("snapshot managed volumes conflict with requested declaration")
	}
	for _, v := range volumes {
		if v.Managed.Persistence != "sync" {
			continue
		}
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
