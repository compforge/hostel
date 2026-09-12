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
	"strings"
	"testing"
	"time"
)

type sequenceCarrier struct {
	snapshots []CarrierSnapshot
	err       error
	next      int
}

func (c *sequenceCarrier) Snapshot() (CarrierSnapshot, error) {
	if c.err != nil {
		return CarrierSnapshot{}, c.err
	}
	snapshot := c.snapshots[c.next]
	c.next++
	return snapshot, nil
}

func TestPressureAdmissionCPUAndMemory(t *testing.T) {
	carrier := &sequenceCarrier{snapshots: []CarrierSnapshot{
		{CPUUsage: 0, CPULimitCores: 1, MemoryCurrentBytes: 50, MemoryLimitBytes: 100},
		{CPUUsage: 950 * time.Millisecond, CPULimitCores: 1, MemoryCurrentBytes: 50, MemoryLimitBytes: 100},
		{CPUUsage: time.Second, CPULimitCores: 1, MemoryCurrentBytes: 95, MemoryLimitBytes: 100},
	}}
	a := newPressureAdmission(carrier, AdmissionConfig{CPUThresholdPercent: 90, MemoryThresholdPercent: 90})
	t0 := time.Unix(100, 0)

	a.sample(t0)
	if decision := a.Check(); !decision.Allowed {
		t.Fatalf("first sample rejected: %+v", decision)
	}
	a.sample(t0.Add(time.Second))
	if decision := a.Check(); decision.Allowed || !strings.Contains(decision.Reason, "CPU usage 95.0%") {
		t.Fatalf("CPU pressure decision = %+v", decision)
	}
	a.sample(t0.Add(2 * time.Second))
	if decision := a.Check(); decision.Allowed || !strings.Contains(decision.Reason, "memory usage 95.0%") {
		t.Fatalf("memory pressure decision = %+v", decision)
	}
	report := a.Report()
	if !report.Available || report.Accepting || !report.CPUAvailable || !report.MemoryAvailable {
		t.Fatalf("report = %+v", report)
	}
}

func TestPressureAdmissionFailsOpenWithoutFiniteLimits(t *testing.T) {
	a := newPressureAdmission(&sequenceCarrier{snapshots: []CarrierSnapshot{{CPUUsage: time.Second}}}, AdmissionConfig{
		CPUThresholdPercent: 90, MemoryThresholdPercent: 90,
	})
	a.sample(time.Unix(100, 0))
	if decision := a.Check(); !decision.Allowed {
		t.Fatalf("unlimited carrier rejected: %+v", decision)
	}
	if report := a.Report(); report.Available || report.Reason != "configured cgroup dimensions have no finite limits" {
		t.Fatalf("unlimited report = %+v", report)
	}
}

func TestPressureAdmissionFailsOpenOnSampleError(t *testing.T) {
	a := newPressureAdmission(&sequenceCarrier{err: errors.New("read failed")}, AdmissionConfig{MemoryThresholdPercent: 90})
	a.sample(time.Unix(100, 0))
	if decision := a.Check(); !decision.Allowed {
		t.Fatalf("sample error rejected: %+v", decision)
	}
	if report := a.Report(); report.Available || !strings.Contains(report.Reason, "read failed") {
		t.Fatalf("sample error report = %+v", report)
	}
}

func TestAdmissionThresholdValidation(t *testing.T) {
	for _, threshold := range []int{-1, 101} {
		if err := validateThreshold("CPU", threshold); err == nil {
			t.Fatalf("validateThreshold(%d): want error", threshold)
		}
	}
}
