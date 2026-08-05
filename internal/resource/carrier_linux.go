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
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type cgroupCarrier struct {
	root string
}

func newCarrier() Carrier {
	carrier, err := newCgroupCarrier(cgroupMount, "/proc/self/cgroup")
	if err != nil {
		return unavailableCarrier{reason: err.Error()}
	}
	return carrier
}

func newCgroupCarrier(mount, selfCgroupFile string) (*cgroupCarrier, error) {
	root, err := carrierRoot(mount, selfCgroupFile)
	if err != nil {
		return nil, err
	}
	return &cgroupCarrier{root: root}, nil
}

// carrierRoot follows hostel into its container cgroup. Once Tracker has
// delegated child groups, hostel itself lives in hostel-system while aggregate
// usage remains on the parent alongside all bed groups.
func carrierRoot(mount, selfCgroupFile string) (string, error) {
	root, err := currentCgroupPath(mount, selfCgroupFile)
	if err != nil {
		return "", err
	}
	if filepath.Base(root) == systemGroup {
		root = filepath.Dir(root)
	}
	return root, nil
}

func currentCgroupPath(mount, selfCgroupFile string) (string, error) {
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

func (c *cgroupCarrier) Snapshot() (CarrierSnapshot, error) {
	cpuStat, err := os.ReadFile(filepath.Join(c.root, "cpu.stat"))
	if err != nil {
		return CarrierSnapshot{}, fmt.Errorf("read carrier cpu.stat: %w", err)
	}
	cpuMax, err := os.ReadFile(filepath.Join(c.root, "cpu.max"))
	if err != nil {
		return CarrierSnapshot{}, fmt.Errorf("read carrier cpu.max: %w", err)
	}
	memoryCurrent, err := os.ReadFile(filepath.Join(c.root, "memory.current"))
	if err != nil {
		return CarrierSnapshot{}, fmt.Errorf("read carrier memory.current: %w", err)
	}
	memoryMax, err := os.ReadFile(filepath.Join(c.root, "memory.max"))
	if err != nil {
		return CarrierSnapshot{}, fmt.Errorf("read carrier memory.max: %w", err)
	}

	usageMicros, err := cpuUsageMicros(string(cpuStat))
	if err != nil {
		return CarrierSnapshot{}, fmt.Errorf("parse carrier CPU usage: %w", err)
	}
	cpuLimit, err := cpuLimitCores(string(cpuMax))
	if err != nil {
		return CarrierSnapshot{}, fmt.Errorf("parse carrier CPU limit: %w", err)
	}
	memoryCurrentBytes, err := strconv.ParseUint(strings.TrimSpace(string(memoryCurrent)), 10, 64)
	if err != nil {
		return CarrierSnapshot{}, fmt.Errorf("parse carrier memory usage: %w", err)
	}
	memoryLimitBytes, err := memoryLimit(string(memoryMax))
	if err != nil {
		return CarrierSnapshot{}, fmt.Errorf("parse carrier memory limit: %w", err)
	}
	return CarrierSnapshot{
		CPUUsage:           time.Duration(usageMicros) * time.Microsecond,
		CPULimitCores:      cpuLimit,
		MemoryCurrentBytes: memoryCurrentBytes,
		MemoryLimitBytes:   memoryLimitBytes,
	}, nil
}

func cpuLimitCores(raw string) (float64, error) {
	fields := strings.Fields(raw)
	if len(fields) != 2 {
		return 0, fmt.Errorf("invalid cpu.max %q", strings.TrimSpace(raw))
	}
	if fields[0] == "max" {
		return 0, nil
	}
	quota, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, err
	}
	period, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, err
	}
	if period == 0 {
		return 0, fmt.Errorf("cpu.max period must be positive")
	}
	return float64(quota) / float64(period), nil
}

func memoryLimit(raw string) (uint64, error) {
	value := strings.TrimSpace(raw)
	if value == "max" {
		return 0, nil
	}
	return strconv.ParseUint(value, 10, 64)
}
