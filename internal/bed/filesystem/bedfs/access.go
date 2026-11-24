package bedfs

import "os"

// PrepareProcessRoot captures the image paths available when this rooted view is prepared.
// +link:doc=docs/volumes.md
func (o *FS) PrepareProcessRoot(readOnly bool) {
	o.ReadOnlyRootFilesystem = readOnly
	o.SystemRoots = nil
	for _, p := range append(SystemPaths(), "/sys") {
		if _, err := os.Stat(p); err == nil {
			o.SystemRoots = append(o.SystemRoots, p)
		}
	}
}

// SystemPaths lists image userland projected into Bed-rooted processes.
// Carrier-rooted processes already see these paths. Shared writable data is a Volume.
func SystemPaths() []string {
	return []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/lib32", "/etc", "/opt"}
}

// ProtectedDirectories are never implicit grants. Filesystem mechanisms may
// mask or exclude them; UID-only execution relies on their Unix permissions.
// The configured beds-root is protected separately by every mechanism.
func ProtectedDirectories() []string {
	return []string{"/root", "/home", "/run/secrets", "/var/run/secrets"}
}
