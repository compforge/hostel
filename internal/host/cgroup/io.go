// Package cgroup provides delegated cgroup v2 hierarchies and allocation handles.
// Names and group organization are chosen by callers.
package cgroup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func unifiedCgroupPath(raw string) (string, error) {
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "0::") {
			path := strings.TrimPrefix(line, "0::")
			if path == "" || !filepath.IsAbs(path) {
				break
			}
			return filepath.Clean(path), nil
		}
	}
	return "", fmt.Errorf("unified cgroup v2 membership not found")
}

func fieldSet(raw string) map[string]bool {
	out := make(map[string]bool)
	for _, field := range strings.Fields(raw) {
		out[strings.TrimPrefix(field, "+")] = true
	}
	return out
}

func moveProcesses(root, target string) error {
	for range 8 {
		raw, err := os.ReadFile(filepath.Join(root, "cgroup.procs"))
		if err != nil {
			return fmt.Errorf("read current cgroup processes: %w", err)
		}
		pids := strings.Fields(string(raw))
		if len(pids) == 0 {
			return nil
		}
		for _, pid := range pids {
			if err := os.WriteFile(filepath.Join(target, "cgroup.procs"), []byte(pid), 0o644); err != nil {
				return fmt.Errorf("move pid %s into shared cgroup: %w", pid, err)
			}
		}
	}
	return fmt.Errorf("current cgroup kept receiving processes during setup")
}

func enableControllers(root string, controllers ...string) error {
	path := filepath.Join(root, "cgroup.subtree_control")
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read cgroup subtree control: %w", err)
	}
	enabled := fieldSet(string(raw))
	var additions []string
	for _, controller := range controllers {
		if !enabled[controller] {
			additions = append(additions, "+"+controller)
		}
	}
	if len(additions) == 0 {
		return nil
	}
	if err := os.WriteFile(path, []byte(strings.Join(additions, " ")), 0o644); err != nil {
		return fmt.Errorf("enable cgroup controllers %v: %w", controllers, err)
	}
	return nil
}

func CPUUsageMicros(raw string) (uint64, error) {
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "usage_usec" {
			return strconv.ParseUint(fields[1], 10, 64)
		}
	}
	return 0, fmt.Errorf("usage_usec missing from cpu.stat")
}

func CurrentPath(mount, selfCgroupFile string) (string, error) {
	raw, err := os.ReadFile(selfCgroupFile)
	if err != nil {
		return "", fmt.Errorf("read cgroup membership: %w", err)
	}
	relative, err := unifiedCgroupPath(string(raw))
	if err != nil {
		return "", err
	}
	return filepath.Join(mount, strings.TrimPrefix(relative, "/")), nil
}
