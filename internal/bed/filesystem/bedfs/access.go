package bedfs

// SoftwareRoot is the shared software installation area. Filesystem prepares
// authorization before Beds execute; no Bed owns its lifecycle.
const SoftwareRoot = "/usr/local"

// DirectoryAccess describes an implicit Carrier grant. Explicit Volume mounts
// carry their own ownership and read-only contracts. Kernel permissions still
// apply even when a directory is granted writable.
type DirectoryAccess struct {
	Path     string
	ReadOnly bool
}

// CommonDirectories owns the shared userland policy used by file APIs and
// process mechanisms. Most-specific paths precede their parents for routing.
// +spec=`Private Bed data, shared userland and management resources have distinct ownership. Mechanisms consume the same grants, and report any protection they cannot realize.`
// +link:doc=docs/volumes.md
func CommonDirectories() []DirectoryAccess {
	return []DirectoryAccess{
		{Path: SoftwareRoot},
		{Path: "/usr", ReadOnly: true}, {Path: "/bin", ReadOnly: true},
		{Path: "/sbin", ReadOnly: true}, {Path: "/lib", ReadOnly: true},
		{Path: "/lib64", ReadOnly: true}, {Path: "/lib32", ReadOnly: true},
		{Path: "/etc", ReadOnly: true}, {Path: "/opt", ReadOnly: true},
		{Path: "/sys", ReadOnly: true},
	}
}

// ProcessDirectories adds kernel interfaces needed by ordinary programs. These
// are process facilities, not ordinary file API data or shared scratch space.
func ProcessDirectories() []DirectoryAccess {
	return append(CommonDirectories(), DirectoryAccess{Path: "/proc", ReadOnly: true}, DirectoryAccess{Path: "/dev"})
}

// ProtectedDirectories are never implicit grants. Filesystem mechanisms may
// mask or exclude them; UID-only execution relies on their Unix permissions.
// The configured beds-root is protected separately by every mechanism.
func ProtectedDirectories() []string {
	return []string{"/root", "/home", "/run/secrets", "/var/run/secrets"}
}
