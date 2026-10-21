package protocol

type PrivilegeStatus struct {
	UID      uint32 `json:"uid"`
	GID      uint32 `json:"gid"`
	Assigned bool   `json:"assigned"`
}

type NetworkStatus struct {
	Enabled bool           `json:"enabled"`
	Gateway string         `json:"gateway"`
	Policy  *NetworkPolicy `json:"policy"`
}

type StoreStatus struct {
	Source             string `json:"source"`
	Restored           bool   `json:"restored"`
	SnapshotGeneration int64  `json:"snapshot_generation"`
	SnapshotBytes      int64  `json:"snapshot_bytes"`
	LocalBytes         int64  `json:"local_bytes"`
}

type ExecutorStatus struct {
	PrivatePIDNamespace bool   `json:"private_pid_namespace"`
	ID                  string `json:"id"`
	Backend             string `json:"backend"`
	State               string `json:"state"`
}

type ResourceStatus struct {
	Accounting bool `json:"accounting"`
}

// FilesystemStatus exposes preparation without leaking host filesystem paths.
type VolumeMountStatus struct {
	Volume        string `json:"volume"`
	Path          string `json:"path"`
	Pillow        string `json:"pillow,omitempty"`
	ReadOnly      bool   `json:"read_only"`
	ProcessMapped bool   `json:"process_mapped"`
	Reason        string `json:"reason,omitempty"`
}

type FilesystemStatus struct {
	Mounts   []VolumeMountStatus `json:"mounts,omitempty"`
	Prepared bool                `json:"prepared"`
}

type NetworkRule struct {
	Action string `json:"action"`
	Target string `json:"target"`
}

type NetworkPolicy struct {
	DefaultAction string        `json:"defaultAction"`
	Egress        []NetworkRule `json:"egress"`
}

type RoomType string

// RoomStatus summarizes attained isolation guarantees.
type RoomStatus struct {
	Requested RoomType `json:"requested"`
	Effective RoomType `json:"effective"`
	Reasons   []string `json:"reasons,omitempty"`
}

const (
	Dorm  RoomType = "dorm"
	Room  RoomType = "room"
	Suite RoomType = "suite"
)
