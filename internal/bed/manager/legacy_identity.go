package manager

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/configuration"
	"github.com/qiankunli/hostel/internal/bed/pillow"
)

var legacyIdentifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

type legacyPathMapping struct {
	HostPath string `json:"host_path"`
	BedPath  string `json:"bed_path"`
	ReadOnly bool   `json:"read_only,omitempty"`
}
type legacyServiceSpec struct {
	Mounts         []model.VolumeMount                  `json:"mounts,omitempty"`
	PortMapping    string                               `json:"port_mapping,omitempty"`
	Name           string                               `json:"name"`
	Command        []string                             `json:"command"`
	Directory      string                               `json:"directory,omitempty"`
	Env            map[string]string                    `json:"env,omitempty"`
	EnvFiles       map[string]string                    `json:"env_files,omitempty"`
	EnvFrom        []string                             `json:"env_from,omitempty"`
	EnvValueFrom   map[string]model.ConfigurationKeyRef `json:"env_value_from,omitempty"`
	Required       bool                                 `json:"required,omitempty"`
	Restart        string                               `json:"restart,omitempty"`
	MaxRestarts    int                                  `json:"max_restarts,omitempty"`
	StartupSeconds int                                  `json:"startup_seconds,omitempty"`
	StopSeconds    int                                  `json:"stop_seconds,omitempty"`
	HTTP           *model.PillowHTTPSpec                `json:"http,omitempty"`
	SpecDigest     string                               `json:"spec_digest,omitempty"`
}

func cloneLegacyServices(in []legacyServiceSpec) []legacyServiceSpec {
	var out []legacyServiceSpec
	raw, _ := json.Marshal(in)
	_ = json.Unmarshal(raw, &out)
	return out
}
func normalizeLegacyServices(specs []legacyServiceSpec) ([]legacyServiceSpec, error) {
	out := cloneLegacyServices(specs)
	seen := make(map[string]bool)
	for i := range out {
		s := &out[i]
		var err error
		s.Mounts, err = model.NormalizeMounts(s.Mounts)
		if err != nil {
			return nil, fmt.Errorf("service %s mounts: %w", s.Name, err)
		}
		providedDigest := s.SpecDigest
		s.SpecDigest = ""
		if !legacyIdentifier.MatchString(s.Name) || seen[s.Name] || len(s.Command) == 0 || s.Command[0] == "" {
			return nil, fmt.Errorf("invalid or duplicate service %q", s.Name)
		}
		seen[s.Name] = true
		if s.Restart == "" {
			s.Restart = "never"
		}
		if !slices.Contains([]string{"never", "on-failure", "always"}, s.Restart) || s.MaxRestarts < 0 || s.MaxRestarts > 100 {
			return nil, fmt.Errorf("invalid restart policy for %s", s.Name)
		}
		if s.MaxRestarts == 0 {
			s.MaxRestarts = 5
		}
		if s.StartupSeconds == 0 {
			s.StartupSeconds = 30
		}
		if s.StopSeconds == 0 {
			s.StopSeconds = 5
		}
		if s.StartupSeconds < 1 || s.StartupSeconds > 120 || s.StopSeconds < 1 || s.StopSeconds > 30 {
			return nil, fmt.Errorf("invalid service deadlines for %s", s.Name)
		}
		if s.Directory == "" {
			s.Directory = "/workspace"
		}
		if err := configuration.Validate(model.Configuration{Env: s.Env, EnvFiles: s.EnvFiles, EnvFrom: s.EnvFrom, EnvValueFrom: s.EnvValueFrom}); err != nil {
			return nil, err
		}
		if s.HTTP != nil {
			if s.PortMapping == "" {
				return nil, fmt.Errorf("HTTP service %s requires a port_mapping", s.Name)
			}
			if !strings.HasPrefix(s.HTTP.ReadyPath, "/") || strings.HasPrefix(s.HTTP.ReadyPath, "//") {
				return nil, fmt.Errorf("HTTP service %s requires readiness path", s.Name)
			}

		}
		raw, _ := json.Marshal(s)
		digest := fmt.Sprintf("%x", sha256.Sum256(raw))
		if providedDigest != "" && providedDigest != digest {
			return nil, fmt.Errorf("service %s definition changed since local creation", s.Name)
		}
		s.SpecDigest = digest
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// +spec=`Verify legacy digests before converting. Only the storage boundary accepts legacy fields.`
// +link:doc=docs/kernel.md
func (r *localIdentityRecord) upgrade() error {
	if r.Pillows != nil {
		if len(r.Services) > 0 || len(r.PathMappings) > 0 || len(r.Mounts) > 0 || len(r.Env) > 0 || len(r.EnvFiles) > 0 || len(r.EnvFrom) > 0 || len(r.EnvValueFrom) > 0 {
			return fmt.Errorf("mixed legacy and pillow declaration")
		}
		var err error
		r.Pillows, err = pillow.Normalize(r.Pillows)
		return err
	}
	services, err := normalizeLegacyServices(r.Services)
	if err != nil {
		return err
	}
	if len(r.PathMappings) > 0 && (len(r.Volumes) > 0 || len(r.Mounts) > 0) {
		return fmt.Errorf("mixed legacy path_mappings and volumes")
	}
	for i, m := range r.PathMappings {
		m.HostPath = filepath.Clean(m.HostPath)
		m.BedPath = path.Clean(m.BedPath)
		name := fmt.Sprintf("legacy-path-%d", i)
		r.Volumes = append(r.Volumes, model.VolumeSpec{Name: name, HostPath: &model.HostPathVolume{Path: m.HostPath}})
		mount := model.VolumeMount{Volume: name, Path: m.BedPath, ReadOnly: m.ReadOnly}
		r.Mounts = append(r.Mounts, mount)
		for j := range services {
			services[j].Mounts = append(services[j].Mounts, mount)
		}
	}
	if _, err := model.NormalizeVolumes(r.Volumes); err != nil {
		return err
	}
	name := "main"
	for i := 0; ; i++ {
		used := false
		for _, s := range services {
			if s.Name == name {
				used = true
				break
			}
		}
		if !used {
			break
		}
		name = fmt.Sprintf("main-%d", i+1)
	}
	r.Pillows = []model.PillowSpec{{Name: name, Mounts: r.Mounts, Env: r.Env, EnvFiles: r.EnvFiles, EnvFrom: r.EnvFrom, EnvValueFrom: r.EnvValueFrom}}
	for _, s := range services {
		r.Pillows = append(r.Pillows, model.PillowSpec{Name: s.Name, Mounts: s.Mounts, Command: s.Command, Workdir: s.Directory, Env: s.Env, EnvFiles: s.EnvFiles, EnvFrom: s.EnvFrom, EnvValueFrom: s.EnvValueFrom, Required: s.Required, RestartPolicy: s.Restart, MaxRestarts: s.MaxRestarts, StartupSeconds: s.StartupSeconds, StopSeconds: s.StopSeconds, HTTP: s.HTTP, PortMapping: s.PortMapping})
	}
	r.Pillows, err = pillow.Normalize(r.Pillows)
	if err != nil {
		return err
	}
	r.Services = nil
	r.PathMappings = nil
	r.Mounts = nil
	r.Env = nil
	r.EnvFiles = nil
	r.EnvFrom = nil
	r.EnvValueFrom = nil
	log.Printf("hostel: migrated local declaration: id=%s pillows=%d", r.ID, len(r.Pillows))
	return nil
}
