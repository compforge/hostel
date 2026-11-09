package protocol

type PrivilegeStatus struct {
	SupplementaryGIDs []int  `json:"supplementary_gids,omitempty"`
	UID               uint32 `json:"uid"`
	GID               uint32 `json:"gid"`
	Assigned          bool   `json:"assigned"`
}

// NetworkDiagnostic attributes a gap or failed operation to its own dimension.
type NetworkDiagnostic struct {
	Code    string `json:"code"`
	Cause   string `json:"cause"`
	Stage   string `json:"stage"`
	Message string `json:"message"`
}

type NetworkIsolationStatus struct {
	Gateway    string             `json:"gateway,omitempty"`
	Level      string             `json:"level"`
	Room       RoomType           `json:"room,omitempty"`
	Diagnostic *NetworkDiagnostic `json:"diagnostic,omitempty"`
}

type EgressState string

const (
	EgressPending    EgressState = "pending"
	EgressEnforced   EgressState = "enforced"
	EgressUnenforced EgressState = "unenforced"
	EgressUnknown    EgressState = "unknown"
	EgressReleased   EgressState = "released"
)

// EgressStatus reports the last confirmed realization, not a connectivity probe.
//
// +spec=`Backend selection does not imply enforcement; effective is null when the installed policy is unknown or absent.`
// +link=`docs/network.md#状态契约`
type EgressStatus struct {
	Backend    string             `json:"backend"`
	State      EgressState        `json:"state"`
	Scope      string             `json:"scope"`
	Requested  *NetworkPolicy     `json:"requested"`
	Effective  *NetworkPolicy     `json:"effective"`
	Diagnostic *NetworkDiagnostic `json:"diagnostic,omitempty"`
}

type NetworkStatus struct {
	Isolation NetworkIsolationStatus `json:"isolation"`
	Egress    EgressStatus           `json:"egress"`
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
	Room     RoomType            `json:"room,omitempty"`
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
	Effective RoomType `json:"effective"`
}

const (
	Dorm  RoomType = "dorm"
	Room  RoomType = "room"
	Suite RoomType = "suite"
)
