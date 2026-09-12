package cgroup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Usage is cumulative CPU and current memory charge for a group and children.
type Usage struct {
	CPUUsage           time.Duration
	MemoryCurrentBytes uint64
}

func (g *Group) Usage() (Usage, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.retiring {
		return Usage{}, os.ErrClosed
	}
	if err := g.current(); err != nil {
		return Usage{}, err
	}
	path := g.path
	cpuRaw, err := os.ReadFile(filepath.Join(path, "cpu.stat"))
	if errors.Is(err, os.ErrNotExist) {
		return Usage{}, nil
	}
	if err != nil {
		return Usage{}, fmt.Errorf("read cgroup %s cpu.stat: %w", path, err)
	}
	memoryRaw, err := os.ReadFile(filepath.Join(path, "memory.current"))
	if err != nil {
		return Usage{}, fmt.Errorf("read cgroup %s memory.current: %w", path, err)
	}
	usageMicros, err := CPUUsageMicros(string(cpuRaw))
	if err != nil {
		return Usage{}, fmt.Errorf("read cgroup %s CPU usage: %w", path, err)
	}
	memoryBytes, err := strconv.ParseUint(strings.TrimSpace(string(memoryRaw)), 10, 64)
	if err != nil {
		return Usage{}, fmt.Errorf("read cgroup %s memory usage: %w", path, err)
	}
	return Usage{
		CPUUsage:           time.Duration(usageMicros) * time.Microsecond,
		MemoryCurrentBytes: memoryBytes,
	}, nil
}
