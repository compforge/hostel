package bed

// Subsystem owns a runtime domain's mechanisms, resources and typed status.
// Each subsystem owns mechanism selection and fallback within its domain.
// It acquires and holds allocations during preparation and ongoing operation,
// then releases them at their owning lifecycle boundary through exact handles.
// Hostel coordinates dependencies to realize BedSpec; a requirement may span domains.
// +spec=`Subsystems own resource acquisition, occupancy, recovery and cleanup, and distinguish reserved resources from realized mechanisms in their status. Hostel coordinates lifecycle order and Bed readiness; callers decide whether realized guarantees meet their needs.`
// +link=component://AGENTS.md#所有权与子系统契约
// +rule=`Failed preparation or cleanup retains allocation ownership until cleanup succeeds. Stop dependent users before release; cleanup must be retryable and scoped to the exact allocation, never a reusable Bed name.`
type Subsystem[S any] interface {
	DaemonLifecycle
	BedLifecycle
	// Status returns an instance snapshot without probes, allocation or remote I/O.
	// Per-Bed observations are published through the subsystem's Bed status writer.
	Status() S
}
