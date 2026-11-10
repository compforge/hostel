package protocol

type PrivilegeStatus struct {
	SupplementaryGIDs []int  `json:"supplementary_gids,omitempty"`
	UID               uint32 `json:"uid"`
	GID               uint32 `json:"gid"`
	Assigned          bool   `json:"assigned"`
}

// NetworkMTUStatus is the startup choice for new private veth pairs, not path MTU.
// Source is not_probed before startup, detected after reading egress interfaces, or fallback.
type NetworkMTUStatus struct {
	Value      int                   `json:"value"`
	Source     string                `json:"source"`
	Interfaces []NetworkMTUInterface `json:"interfaces,omitempty"`
	Reason     string                `json:"reason,omitempty"`
}

type NetworkMTUInterface struct {
	Name string `json:"name"`
	MTU  int    `json:"mtu"`
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

// FilesystemRealization records the last confirmed Bed preparation/cleanup.
// It is not a new host probe or a promise that external mutations are detected.
// +spec=`Only prepared realization reports active guarantees. Partial cleanup clears those claims and retains an unknown state until release succeeds.`
// +link:doc=docs/isolation.md
type FilesystemRealization struct {
	State  string `json:"state"` // pending, prepared, unknown, released
	Reason string `json:"reason,omitempty"`
	// PrivateData names the actual protection: uid, bwrap, landlock, or none.
	PrivateData string `json:"private_data,omitempty"`
	// CoreAccess distinguishes path rules from Unix-permission-only protection.
	CoreAccess string                 `json:"core_access,omitempty"`
	FileAPI    string                 `json:"file_api,omitempty"`
	Views      []FilesystemViewStatus `json:"views,omitempty"`
	Software   []SoftwareAccessStatus `json:"software,omitempty"`
}

type FilesystemViewStatus struct {
	Pillow          string `json:"pillow"`
	Mode            string `json:"mode"`
	Rootfs          bool   `json:"rootfs"`
	ReadWriteMounts bool   `json:"read_write_mounts"`
	ReadOnlyMounts  bool   `json:"read_only_mounts"`
	Reason          string `json:"reason,omitempty"`
}

// Software access describes runtime preparation and identity authorization;
// explicit file modes, named ACLs and application chmod can still deny an operation.
type SoftwareAccessStatus struct {
	Path       string `json:"path"`
	Prepared   bool   `json:"prepared"`
	Authorized bool   `json:"authorized"`
	Reason     string `json:"reason,omitempty"`
}

type FilesystemStatus struct {
	Realization *FilesystemRealization `json:"realization,omitempty"`
	Room        RoomType               `json:"room,omitempty"`
	Mounts      []VolumeMountStatus    `json:"mounts,omitempty"`
	Prepared    bool                   `json:"prepared"`
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
