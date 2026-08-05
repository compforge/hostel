//go:build linux

// Copyright 2026 Li Qiankun
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package resource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	cgroupMount    = "/sys/fs/cgroup"
	systemGroup    = "hostel-system"
	bedGroupPrefix = "hostel-bed-"
)

type cgroupTracker struct {
	root string
}

func newTracker() Tracker {
	tracker, err := setupCgroupTracker(cgroupMount, "/proc/self/cgroup")
	if err != nil {
		return Noop(err.Error())
	}
	return tracker
}

// setupCgroupTracker creates sibling groups for hostel-system and each bed.
// Moving the container's existing processes into hostel-system makes the
// current cgroup an empty domain parent, allowing cpu/memory controllers to be
// delegated to bed children without applying any limits.
func setupCgroupTracker(mount, selfCgroupFile string) (*cgroupTracker, error) {
	current, err := currentCgroupPath(mount, selfCgroupFile)
	if err != nil {
		return nil, err
	}
	root := current
	system := filepath.Join(root, systemGroup)
	if filepath.Base(current) == systemGroup {
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
	return &cgroupTracker{root: root}, nil
}

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

func (t *cgroupTracker) Report() Report {
	return Report{Backend: "cgroupv2", Available: true}
}

func (t *cgroupTracker) groupPath(bedID string) string {
	// bed.Manager validates ids before any process can reach the tracker; its
	// grammar excludes path separators, so the id remains a single cgroup name.
	return filepath.Join(t.root, bedGroupPrefix+bedID)
}

func (t *cgroupTracker) OpenGroup(bedID string) (*os.File, error) {
	path := t.groupPath(bedID)
	if err := os.Mkdir(path, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create cgroup for bed %s: %w", bedID, err)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open cgroup for bed %s: %w", bedID, err)
	}
	return file, nil
}

func (t *cgroupTracker) Usage(bedID string) (Usage, error) {
	path := t.groupPath(bedID)
	cpuRaw, err := os.ReadFile(filepath.Join(path, "cpu.stat"))
	if errors.Is(err, os.ErrNotExist) {
		return Usage{}, nil
	}
	if err != nil {
		return Usage{}, fmt.Errorf("read bed %s cpu.stat: %w", bedID, err)
	}
	memoryRaw, err := os.ReadFile(filepath.Join(path, "memory.current"))
	if err != nil {
		return Usage{}, fmt.Errorf("read bed %s memory.current: %w", bedID, err)
	}
	usageMicros, err := cpuUsageMicros(string(cpuRaw))
	if err != nil {
		return Usage{}, fmt.Errorf("read bed %s CPU usage: %w", bedID, err)
	}
	memoryBytes, err := strconv.ParseUint(strings.TrimSpace(string(memoryRaw)), 10, 64)
	if err != nil {
		return Usage{}, fmt.Errorf("read bed %s memory usage: %w", bedID, err)
	}
	return Usage{
		CPUUsage:           time.Duration(usageMicros) * time.Microsecond,
		MemoryCurrentBytes: memoryBytes,
	}, nil
}

func cpuUsageMicros(raw string) (uint64, error) {
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "usage_usec" {
			return strconv.ParseUint(fields[1], 10, 64)
		}
	}
	return 0, fmt.Errorf("usage_usec missing from cpu.stat")
}

func (t *cgroupTracker) Release(bedID string) error {
	path := t.groupPath(bedID)
	if err := os.WriteFile(filepath.Join(path, "cgroup.kill"), []byte("1"), 0o644); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		// Older cgroup v2 kernels may lack cgroup.kill; the spawner already
		// killed the tree, so continue to the populated/rmdir check.
	}
	for range 20 {
		err := os.Remove(path)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if !errors.Is(err, syscall.EBUSY) {
			return fmt.Errorf("remove cgroup for bed %s: %w", bedID, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("remove cgroup for bed %s: still populated", bedID)
}
