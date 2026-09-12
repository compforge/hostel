package bed

import "time"

type LifecyclePhase string

const (
	PhaseInitializing LifecyclePhase = "initializing"
	PhaseResident     LifecyclePhase = "resident"
	PhaseEvicting     LifecyclePhase = "evicting"
	PhasePurging      LifecyclePhase = "purging"
	PhaseDormant      LifecyclePhase = "dormant"
	PhaseFailed       LifecyclePhase = "failed"
)

// Status contains observations for one Bed, never live resources, clients or
// goroutines. Each domain owns its section; instance-wide Component Status is
// reported separately through Component.Status().
type Status struct {
	Lifecycle  LifecycleStatus
	Filesystem FilesystemStatus
	Privilege  PrivilegeStatus
	Network    NetworkStatus
	Store      StoreStatus
	Executor   ExecutorStatus
	Resource   ResourceStatus
}
type LifecycleStatus struct {
	Phase     LifecyclePhase `json:"phase"`
	Ready     bool           `json:"ready"`
	Reason    string         `json:"reason"`
	Message   string         `json:"message"`
	UpdatedAt time.Time      `json:"updated_at"`
}
type FilesystemStatus struct {
	Home      string `json:"-"`
	Workspace string `json:"-"`
	Prepared  bool   `json:"prepared"`
}
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
	ID      string `json:"id"`
	Backend string `json:"backend"`
	State   string `json:"state"`
}
type ResourceStatus struct {
	Accounting bool `json:"accounting"`
}

func cloneStatus(s Status) Status { s.Network.Policy = clonePolicy(s.Network.Policy); return s }

// StatusWriter grants access to one typed section. Update works on detached
// values so retained callback pointers cannot mutate the published snapshot.
type StatusWriter[T any] struct {
	section func(*Status) *T
	clone   func(T) T
}

func (w StatusWriter[T]) Update(b *Bed, change func(*T)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := w.section(&b.status)
	v := w.clone(*p)
	change(&v)
	*p = w.clone(v)
}
func (w StatusWriter[T]) Set(b *Bed, value T) { w.Update(b, func(v *T) { *v = value }) }
func writer[T any](section func(*Status) *T) StatusWriter[T] {
	return StatusWriter[T]{section: section, clone: func(v T) T { return v }}
}

// Owners is assembled once and distributed one field at a time to domains.
// There is deliberately no whole-Status writer.
type Owners struct {
	Lifecycle  StatusWriter[LifecycleStatus]
	Filesystem StatusWriter[FilesystemStatus]
	Privilege  StatusWriter[PrivilegeStatus]
	Network    StatusWriter[NetworkStatus]
	Store      StatusWriter[StoreStatus]
	Executor   StatusWriter[ExecutorStatus]
	Resource   StatusWriter[ResourceStatus]
}

func NewOwners() Owners {
	network := writer(func(s *Status) *NetworkStatus { return &s.Network })
	network.clone = func(s NetworkStatus) NetworkStatus { s.Policy = clonePolicy(s.Policy); return s }
	return Owners{
		Lifecycle:  writer(func(s *Status) *LifecycleStatus { return &s.Lifecycle }),
		Filesystem: writer(func(s *Status) *FilesystemStatus { return &s.Filesystem }),
		Privilege:  writer(func(s *Status) *PrivilegeStatus { return &s.Privilege }),
		Network:    network,
		Store:      writer(func(s *Status) *StoreStatus { return &s.Store }),
		Executor:   writer(func(s *Status) *ExecutorStatus { return &s.Executor }),
		Resource:   writer(func(s *Status) *ResourceStatus { return &s.Resource }),
	}
}
