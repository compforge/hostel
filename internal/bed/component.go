package bed

// Component owns its lifecycle and typed capability report. Capability grades,
// requirements and fallback candidates remain inside their respective domains.
type Component[R any] interface {
	DaemonLifecycle
	BedLifecycle
	// Status returns a snapshot without host probes or remote I/O.
	Status() R
}
