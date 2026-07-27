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
)

// Usage is one cumulative cgroup snapshot. CPUUsage includes processes that
// already exited; MemoryCurrentBytes is the group's current resident charge.
type Usage struct {
	CPUUsage           time.Duration
	MemoryCurrentBytes uint64
}

// Report describes whether exact per-bed accounting is active.
type Report struct {
	Backend   string
	Available bool
	Reason    string
}

// Tracker prepares per-bed accounting groups and reads their cumulative usage.
// OpenGroup returns a cgroup directory fd suitable for CLONE_INTO_CGROUP; nil
// means accounting is unavailable on this host.
type Tracker interface {
	Report() Report
	OpenGroup(bedID string) (*os.File, error)
	Usage(bedID string) (Usage, error)
	Release(bedID string) error
}

// New probes cgroup v2 accounting and degrades to a truthful noop tracker when
// the hierarchy is absent or not delegated to the container.
func New() Tracker { return newTracker() }

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
