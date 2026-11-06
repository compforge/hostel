package protocol

// PillowSpec is the immutable, non-secret definition of a named execution environment
// and its optional supervised main process. A Bed must explicitly declare at least
// one Pillow; its first declaration is the default. Runtime addresses never enter it.
type PillowSpec struct {
	Ports          []PillowPort                   `json:"ports,omitempty"`
	ReadinessProbe *Probe                         `json:"readinessProbe,omitempty"`
	Mounts         []VolumeMount                  `json:"mounts,omitempty"`
	Name           string                         `json:"name"`
	Args           []string                       `json:"args,omitempty"`
	Command        []string                       `json:"command,omitempty"`
	Workdir        string                         `json:"workdir,omitempty"`
	Env            map[string]string              `json:"env,omitempty"`
	EnvFiles       map[string]string              `json:"env_files,omitempty"`
	EnvFrom        []string                       `json:"env_from,omitempty"`
	EnvValueFrom   map[string]ConfigurationKeyRef `json:"env_value_from,omitempty"`
	Required       bool                           `json:"required,omitempty"`
	RestartPolicy  string                         `json:"restart_policy,omitempty"`
	MaxRestarts    int                            `json:"max_restarts,omitempty"`
	StopSeconds    int                            `json:"stop_seconds,omitempty"`
	SpecDigest     string                         `json:"spec_digest,omitempty"`
}

type VolumeSpec struct {
	Name     string          `json:"name"`
	Managed  *ManagedVolume  `json:"managed,omitempty"`
	HostPath *HostPathVolume `json:"host_path,omitempty"`
}

// ManagedVolume is allocated and reclaimed by Hostel.
// +spec=`sync requires a durable Store; none is discarded when the Bed is evicted.`
type ManagedVolume struct {
	Persistence string `json:"persistence"`
}

// HostPath is resolved in the daemon filesystem, including its container mounts.
type HostPathVolume struct {
	Path string `json:"path"`
}

// VolumeMount grants one consumer access to a named Bed volume.
// +spec=`ReadOnly applies to file APIs and the consumer's process view; it cannot silently degrade into writable access.`
type VolumeMount struct {
	Volume   string `json:"volume"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

type ConfigurationKeyRef struct {
	Source string `json:"source"`
	Key    string `json:"key"`
}
