package filesystem

// Mount describes one ordered bubblewrap filesystem operation. Later mounts
// cover earlier ones; callers must put masks before the paths they expose.
type Mount struct {
	Kind           MountKind
	Source, Target string
}
type MountKind string

const (
	Bind         MountKind = "--bind"
	ReadOnlyBind MountKind = "--ro-bind"
	Tmpfs        MountKind = "--tmpfs"
	Dev          MountKind = "--dev"
	Directory    MountKind = "--dir"
)

// Bubblewrap describes the namespace and mount shape of a process. It selects
// no default mounts or paths; the caller owns the isolation policy.
type Bubblewrap struct {
	UserNamespace, UTSNamespace, IPCNamespace bool
	Mounts                                    []Mount
	Cwd                                       string
	DieWithParent                             bool
}

func (b Bubblewrap) Args() []string {
	var args []string
	if b.UserNamespace {
		args = append(args, "--unshare-user")
	}
	if b.UTSNamespace {
		args = append(args, "--unshare-uts")
	}
	if b.IPCNamespace {
		args = append(args, "--unshare-ipc")
	}
	for _, m := range b.Mounts {
		args = append(args, string(m.Kind))
		if m.Kind == Bind || m.Kind == ReadOnlyBind {
			args = append(args, m.Source)
		}
		args = append(args, m.Target)
	}
	if b.Cwd != "" {
		args = append(args, "--chdir", b.Cwd)
	}
	if b.DieWithParent {
		args = append(args, "--die-with-parent")
	}
	return append(args, "--")
}
