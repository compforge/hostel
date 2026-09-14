package bed

// Level maps a domain-owned isolation grade to the highest Bed profile whose
// requirements it satisfies. Ordering and selection remain inside the domain.
type Level interface {
	Room() RoomType
}

// LevelStatus contains confirmed capability facts, not user expectations or a
// runtime selection. Config and composition validation determine the latter.
// An empty Supported means this component does not grade isolation; a component
// with only baseline isolation must explicitly report its baseline Level.
type LevelStatus struct {
	Supported []Level `json:"supported"`
}

type Component[R any] interface {
	DaemonLifecycle
	BedLifecycle
	// Status returns this domain's instance-wide snapshot, distinct from its
	// per-Bed section in Bed.Status(). R remains the domain-owned report type.
	// Like LevelStatus, it performs no host probes or remote I/O.
	Status() R
	// LevelStatus returns a snapshot without host probes or remote I/O.
	LevelStatus() LevelStatus
}
