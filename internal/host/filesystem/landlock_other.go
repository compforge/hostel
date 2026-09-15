//go:build !linux

package filesystem

import "fmt"

func RestrictPaths([]string, []string, []string) error { return fmt.Errorf("Landlock requires Linux") }
