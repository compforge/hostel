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

// Package resource owns per-bed resource accounting. It deliberately exposes
// cumulative usage rather than an HTTP-shaped percentage: callers choose their
// own sampling window while the cgroup remains the accounting source of truth.
package resource

import (
	"os"
	"time"

	"github.com/qiankunli/hostel/internal/feature"
)

// Usage is one cumulative cgroup snapshot. CPUUsage includes processes that
// already exited; MemoryCurrentBytes is the group's current resident charge.
type Usage struct {
	CPUUsage           time.Duration
	MemoryCurrentBytes uint64
}

// Report describes whether exact per-bed accounting is active.
type Report struct {
	Features  map[string]feature.Status `json:"features,omitempty"`
	Backend   string                    `json:"backend"`
	Available bool                      `json:"available"`
	Reason    string                    `json:"reason,omitempty"`
}

// Tracker prepares per-bed accounting groups and reads their cumulative usage.
// OpenGroup prepares and opens the empty Bed accounting parent; nil means
// accounting is unavailable. Only ExecutorGroup children receive processes.
type Tracker interface {
	Report() Report
	OpenGroup(bedID string) (*os.File, error)
	ExecutorGroup(bedID, executorID string) (Group, error)
	Usage(bedID string) (Usage, error)
	Release(bedID string) error
}

// New assembles accounting without touching cgroups. Resource Manager.Start
// drives the host probe and records optional accounting availability.
func New() Tracker { return NewConfigured(Config{}) }
func NewConfigured(cfg Config) Tracker {
	return &hostTracker{config: cfg, tracker: Noop("not started"), create: newTracker}
}

// Noop is used before cmd/hostel wires the host tracker and by non-Linux hosts.
func Noop(reason string) Tracker {
	return &noopTracker{reason: reason}
}

type noopTracker struct {
	reason string
}

func (t *noopTracker) Report() Report {
	return Report{Backend: "noop", Available: false, Reason: t.reason}
}

func (t *noopTracker) OpenGroup(string) (*os.File, error) { return nil, nil }
func (t *noopTracker) Usage(string) (Usage, error)        { return Usage{}, nil }
func (t *noopTracker) Release(string) error               { return nil }

// Group is one Executor allocation below a resident Bed's accounting parent.
// Its exact identity prevents delayed cleanup from touching a replacement.
type Group interface {
	Open() (*os.File, error)
	Close() error
}
type noopGroup struct{}

func (noopGroup) Open() (*os.File, error)                        { return nil, nil }
func (noopGroup) Close() error                                   { return nil }
func (*noopTracker) ExecutorGroup(string, string) (Group, error) { return noopGroup{}, nil }
