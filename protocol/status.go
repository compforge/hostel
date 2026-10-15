package protocol

// BedStatus groups observations by the domain that owns each fact.
type BedStatus struct {
	Services     []ServiceStatus     `json:"services"`
	PortMappings []PortMappingStatus `json:"port_mappings"`
	Isolation    RoomStatus          `json:"isolation"`
	Lifecycle    BedLifecycle        `json:"lifecycle"`
	Components   struct {
		Filesystem FilesystemStatus `json:"filesystem"`
		Privilege  PrivilegeStatus  `json:"privilege"`
		Network    NetworkStatus    `json:"network"`
		Store      StoreStatus      `json:"store"`
		Executor   ExecutorStatus   `json:"executor"`
		Resource   ResourceStatus   `json:"resource"`
	} `json:"components"`
	Amenities map[string]AmenityBinding `json:"amenities"`
}

// AmenityBinding carries facility-specific diagnostics. Status is a JSON value
// whose schema is owned by that facility, rather than an internal Tenant interface.
type AmenityBinding struct {
	TenantID string `json:"tenant_id"`
	Status   any    `json:"status"`
}
