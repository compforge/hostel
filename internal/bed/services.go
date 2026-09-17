package bed

import (
	"maps"
	"slices"
)

// ServiceSpec is the immutable, non-secret definition of a process supervised
// with its Bed. Runtime addresses and generated credentials never enter it.
type ServiceSpec struct {
	PortMapping    string                         `json:"port_mapping,omitempty"`
	Name           string                         `json:"name"`
	Command        []string                       `json:"command"`
	Directory      string                         `json:"directory,omitempty"`
	Env            map[string]string              `json:"env,omitempty"`
	EnvFiles       map[string]string              `json:"env_files,omitempty"`
	EnvFrom        []string                       `json:"env_from,omitempty"`
	EnvValueFrom   map[string]ConfigurationKeyRef `json:"env_value_from,omitempty"`
	Required       bool                           `json:"required,omitempty"`
	Restart        string                         `json:"restart,omitempty"`
	MaxRestarts    int                            `json:"max_restarts,omitempty"`
	StartupSeconds int                            `json:"startup_seconds,omitempty"`
	StopSeconds    int                            `json:"stop_seconds,omitempty"`
	HTTP           *ServiceHTTPSpec               `json:"http,omitempty"`
	SpecDigest     string                         `json:"spec_digest,omitempty"`
}

type ServiceHTTPSpec struct {
	ReadyPath string `json:"ready_path"`
	// Authentication is optional; nil publishes HTTP without credentials.
	Authentication *Authentication `json:"authentication,omitempty"`
}

// Authentication describes credentials used by both readiness and service access.
// The service process itself is responsible for enforcing authentication.
type Authentication struct {
	Scheme      string      `json:"scheme"`
	TokenSource TokenSource `json:"token_source"`
	TokenEnv    string      `json:"token_env"`
}

type TokenSource string

const (
	// TokenSourceGenerated rotates the credential for every service execution.
	TokenSourceGenerated TokenSource = "generated"
	// TokenSourceEnvironment reads the service's resolved Env/EnvFiles value.
	TokenSourceEnvironment TokenSource = "environment"
)

func CloneServices(in []ServiceSpec) []ServiceSpec {
	out := make([]ServiceSpec, len(in))
	for i, s := range in {
		s.Command = slices.Clone(s.Command)
		s.Env = maps.Clone(s.Env)
		s.EnvFiles = maps.Clone(s.EnvFiles)
		s.EnvFrom = slices.Clone(s.EnvFrom)
		s.EnvValueFrom = maps.Clone(s.EnvValueFrom)
		if s.HTTP != nil {
			http := *s.HTTP
			if http.Authentication != nil {
				auth := *http.Authentication
				http.Authentication = &auth
			}
			s.HTTP = &http
		}
		out[i] = s
	}
	return out
}
