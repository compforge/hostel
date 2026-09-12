package bed

// Status contains observations, never live resources, clients or goroutines.
type Status struct {
	Lifecycle  LifecycleStatus
	Filesystem FilesystemStatus
	Privilege  PrivilegeStatus
	Network    NetworkStatus
	Store      StoreStatus
	Executor   ExecutorStatus
	Resource   ResourceStatus
	Amenity    AmenityStatus
}
type LifecycleStatus struct {
	Phase  string
	Ready  bool
	Reason string
}
type FilesystemStatus struct {
	Home      string
	Workspace string
	Prepared  bool
}
type PrivilegeStatus struct {
	UID      uint32
	GID      uint32
	Assigned bool
}
type NetworkStatus struct {
	Enabled bool
	Gateway string
	Policy  *NetworkPolicy
}
type StoreStatus struct {
	Source        string
	Restored      bool
	Generation    int64
	SnapshotBytes int64
}
type ExecutorStatus struct {
	ID      string
	Backend string
	State   string
}
type ResourceStatus struct{ Accounting bool }
type AmenityStatus struct{ Released bool }

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
	Amenity    StatusWriter[AmenityStatus]
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
		Amenity:    writer(func(s *Status) *AmenityStatus { return &s.Amenity }),
	}
}
