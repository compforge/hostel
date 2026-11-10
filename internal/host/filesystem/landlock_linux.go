//go:build linux

package filesystem

import (
	"fmt"
	"os"

	ll "github.com/landlock-lsm/go-landlock/landlock"
	lls "github.com/landlock-lsm/go-landlock/landlock/syscall"
)

// RestrictPaths restricts the calling process using the available Landlock ABI.
// Nonexistent paths are ignored. Selection may fall back to another mechanism,
// but execution must never silently drop a selected restriction.
func RestrictPaths(readOnly, readWrite, executableFiles []string) error {
	abi, err := lls.LandlockGetABIVersion()
	if err != nil {
		return fmt.Errorf("query Landlock ABI: %w", err)
	}
	config, err := landlockConfig(int(abi))
	if err != nil {
		return err
	}
	existing := func(paths []string) []string {
		var out []string
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				out = append(out, p)
			}
		}
		return out
	}
	return config.RestrictPaths(ll.RODirs(existing(readOnly)...), ll.RWDirs(existing(readWrite)...), ll.ROFiles(executableFiles...))
}

// Choose the strongest supported preset without BestEffort's ABI-zero no-op.
// RestrictPaths performs its own ABI check and must fail if support disappears.
func landlockConfig(abi int) (ll.Config, error) {
	presets := [...]ll.Config{ll.V1, ll.V2, ll.V3, ll.V4, ll.V5, ll.V6, ll.V7, ll.V8, ll.V9}
	if abi < 1 {
		return ll.Config{}, fmt.Errorf("Landlock ABI unavailable: %d", abi)
	}
	return presets[min(abi, len(presets))-1], nil
}
