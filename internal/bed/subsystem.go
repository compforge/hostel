package bed

// Subsystem owns a runtime domain's mechanisms, resources and typed status.
// Each subsystem owns mechanism selection and fallback within its domain.
// Hostel coordinates dependencies to realize BedSpec; a requirement may span domains.
// +spec=`Subsystems own resource cleanup and publish observed facts. Hostel coordinates lifecycle order and Bed readiness; callers decide whether realized guarantees meet their needs.`
// +link=component://docs/kernel.md
// +rule=`Failed preparation may retain allocations. Cleanup must be retryable and scoped to the exact allocation, never a reusable Bed name.`
type Subsystem[S any] interface {
	DaemonLifecycle
	BedLifecycle
	// Status returns an instance snapshot without probes, allocation or remote I/O.
	// Per-Bed observations are published through the subsystem's Bed status writer.
	Status() S
}
