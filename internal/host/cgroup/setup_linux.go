//go:build linux

package cgroup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Setup delegates cpu/memory controllers without setting limits. It moves
// existing processes into the caller-named child so the parent can be empty.
// The returned root is borrowed; closing it never removes the carrier cgroup.
func Setup(mount, selfCgroupFile, processGroup string) (*Group, error) {
	if err := validateName(processGroup); err != nil {
		return nil, err
	}
	current, err := CurrentPath(mount, selfCgroupFile)
	if err != nil {
		return nil, err
	}
	root := current
	system := filepath.Join(root, processGroup)
	if filepath.Base(current) == processGroup {
		root = filepath.Dir(current)
		system = current
	}

	controllers, err := os.ReadFile(filepath.Join(root, "cgroup.controllers"))
	if err != nil {
		return nil, fmt.Errorf("read cgroup controllers: %w", err)
	}
	available := fieldSet(string(controllers))
	for _, controller := range []string{"cpu", "memory"} {
		if !available[controller] {
			return nil, fmt.Errorf("cgroup v2 controller %s is not delegated", controller)
		}
	}

	if err := os.Mkdir(system, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create shared process cgroup: %w", err)
	}
	if current != system {
		if err := moveProcesses(root, system); err != nil {
			return nil, err
		}
	}
	if err := enableControllers(root, "cpu", "memory"); err != nil {
		return nil, err
	}
	return openGroup(root, false)
}
