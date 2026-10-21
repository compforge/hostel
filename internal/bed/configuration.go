package bed

import (
	"maps"
	"slices"

	"github.com/qiankunli/hostel/protocol"
)

// Configuration describes process configuration without resolving file values.
// EnvFrom names deployment-registered directories, not arbitrary host paths.
type Configuration struct {
	Env          map[string]string              `json:"env,omitempty"`
	EnvFiles     map[string]string              `json:"env_files,omitempty"`
	EnvFrom      []string                       `json:"env_from,omitempty"`
	EnvValueFrom map[string]ConfigurationKeyRef `json:"env_value_from,omitempty"`
}

type ConfigurationKeyRef = protocol.ConfigurationKeyRef

func CloneConfiguration(s Configuration) Configuration {
	s.Env = maps.Clone(s.Env)
	s.EnvFiles = maps.Clone(s.EnvFiles)
	s.EnvFrom = slices.Clone(s.EnvFrom)
	s.EnvValueFrom = maps.Clone(s.EnvValueFrom)
	return s
}

func PillowConfiguration(s PillowSpec) Configuration {
	return CloneConfiguration(Configuration{Env: s.Env, EnvFiles: s.EnvFiles, EnvFrom: s.EnvFrom, EnvValueFrom: s.EnvValueFrom})
}
