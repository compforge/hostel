package bed

import (
	"maps"
	"slices"
)

// Configuration describes process configuration without resolving file values.
// EnvFrom names deployment-registered directories, not arbitrary host paths.
type Configuration struct {
	Env          map[string]string              `json:"env,omitempty"`
	EnvFiles     map[string]string              `json:"env_files,omitempty"`
	EnvFrom      []string                       `json:"env_from,omitempty"`
	EnvValueFrom map[string]ConfigurationKeyRef `json:"env_value_from,omitempty"`
}

type ConfigurationKeyRef struct {
	Source string `json:"source"`
	Key    string `json:"key"`
}

func CloneConfiguration(s Configuration) Configuration {
	s.Env = maps.Clone(s.Env)
	s.EnvFiles = maps.Clone(s.EnvFiles)
	s.EnvFrom = slices.Clone(s.EnvFrom)
	s.EnvValueFrom = maps.Clone(s.EnvValueFrom)
	return s
}

func (s Spec) MainConfiguration() Configuration {
	return CloneConfiguration(Configuration{Env: s.Env, EnvFiles: s.EnvFiles, EnvFrom: s.EnvFrom, EnvValueFrom: s.EnvValueFrom})
}

func (s ServiceSpec) Configuration() Configuration {
	return CloneConfiguration(Configuration{Env: s.Env, EnvFiles: s.EnvFiles, EnvFrom: s.EnvFrom, EnvValueFrom: s.EnvValueFrom})
}
