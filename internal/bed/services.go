package bed

import (
	"maps"
	"slices"
)

// ServiceSpec is the immutable, non-secret definition of a process supervised
// with its Bed. Runtime addresses and generated credentials never enter it.
type ServiceSpec struct {
	Name           string            `json:"name"`
	Command        []string          `json:"command"`
	Directory      string            `json:"directory,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	EnvFiles       map[string]string `json:"env_files,omitempty"`
	Required       bool              `json:"required,omitempty"`
	Restart        string            `json:"restart,omitempty"`
	MaxRestarts    int               `json:"max_restarts,omitempty"`
	StartupSeconds int               `json:"startup_seconds,omitempty"`
	StopSeconds    int               `json:"stop_seconds,omitempty"`
	HTTP           *ServiceHTTPSpec  `json:"http,omitempty"`
	SpecDigest     string            `json:"spec_digest,omitempty"`
}

type ServiceHTTPSpec struct {
	ReadyPath string `json:"ready_path"`
	// TokenEnv receives a fresh credential for every service execution.
	TokenEnv string `json:"token_env"`
}

func CloneServices(in []ServiceSpec) []ServiceSpec {
	out := make([]ServiceSpec, len(in))
	for i, s := range in {
		s.Command = slices.Clone(s.Command)
		s.Env = maps.Clone(s.Env)
		s.EnvFiles = maps.Clone(s.EnvFiles)
		if s.HTTP != nil {
			http := *s.HTTP
			s.HTTP = &http
		}
		out[i] = s
	}
	return out
}
