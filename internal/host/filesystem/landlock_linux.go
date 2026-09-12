//go:build linux

package filesystem

import (
	"os"

	ll "github.com/landlock-lsm/go-landlock/landlock"
)

// RestrictPaths restricts the calling process using the available Landlock ABI.
// Nonexistent paths are ignored. Callers must probe their complete allowance
// set to establish enforcement before relying on this best-effort mechanism.
func RestrictPaths(readOnly, readWrite []string) error {
	existing := func(paths []string) []string {
		var out []string
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				out = append(out, p)
			}
		}
		return out
	}
	return ll.V9.BestEffort().RestrictPaths(ll.RODirs(existing(readOnly)...), ll.RWDirs(existing(readWrite)...))
}
