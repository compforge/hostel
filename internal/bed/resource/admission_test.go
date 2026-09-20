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
	"context"
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
	a := newPressureAdmission(carrier, AdmissionConfig{
		CPUPressureThresholdPercent: 80, MemoryPressureThresholdPercent: 80,
		CPUThresholdPercent: 90, MemoryThresholdPercent: 90,
	})
	t0 := time.Unix(100, 0)

	a.sample(t0)
	if decision := a.Check(); !decision.Allowed {
		t.Fatalf("first sample rejected: %+v", decision)
	}
	a.sample(t0.Add(time.Second))
	if decision := a.Check(); decision.Allowed || !strings.Contains(decision.Reason, "CPU usage 95.0%") {
		t.Fatalf("CPU pressure decision = %+v", decision)
	}
	if report := a.Report(); !report.CPUPressure || report.MemoryPressure {
		t.Fatalf("CPU signal: %+v", report)
	}
	a.sample(t0.Add(2 * time.Second))
	if decision := a.Check(); decision.Allowed || !strings.Contains(decision.Reason, "memory usage 95.0%") {
		t.Fatalf("memory pressure decision = %+v", decision)
	}
	report := a.Report()
	if !report.Available || report.Accepting || !report.CPUAvailable || !report.MemoryAvailable || report.CPUPressure || !report.MemoryPressure {
		t.Fatalf("report = %+v", report)
	}
}

func TestPressureSignalsPrecedeAdmission(t *testing.T) {
	carrier := &sequenceCarrier{snapshots: []CarrierSnapshot{
		{CPUUsage: 0, CPULimitCores: 1, MemoryCurrentBytes: 85, MemoryLimitBytes: 100},
		{CPUUsage: 850 * time.Millisecond, CPULimitCores: 1, MemoryCurrentBytes: 85, MemoryLimitBytes: 100},
	}}
	a := newPressureAdmission(carrier, AdmissionConfig{
		CPUPressureThresholdPercent: 80, MemoryPressureThresholdPercent: 80,
		CPUThresholdPercent: 90, MemoryThresholdPercent: 90,
	})
	now := time.Unix(100, 0)
	a.sample(now)
	if report := a.Report(); !report.MemoryPressure || report.CPUPressure || !report.Accepting {
		t.Fatalf("first soft-pressure report = %+v", report)
	}
	a.sample(now.Add(time.Second))
	if report := a.Report(); !report.CPUPressure || !report.MemoryPressure || !report.Accepting {
		t.Fatalf("soft pressure must precede admission = %+v", report)
	}
	if decision := a.Check(); !decision.Allowed {
		t.Fatalf("soft pressure rejected work: %+v", decision)
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
	for _, tc := range []struct {
		pressure  int
		admission int
	}{
		{80, 80},
		{90, 80},
	} {
		if err := validatePressureBeforeAdmission("CPU", tc.pressure, tc.admission); err == nil {
			t.Fatalf("validatePressureBeforeAdmission(%d, %d): want error", tc.pressure, tc.admission)
		}
	}
}

func TestNewAdmissionRejectsPressureThatDoesNotPrecedeAdmission(t *testing.T) {
	for _, cfg := range []AdmissionConfig{
		{CPUPressureThresholdPercent: 90, CPUThresholdPercent: 90},
		{MemoryPressureThresholdPercent: 95, MemoryThresholdPercent: 90},
	} {
		if _, err := NewAdmission(context.Background(), &sequenceCarrier{}, cfg); err == nil {
			t.Fatalf("NewAdmission(%+v): want soft-before-hard validation error", cfg)
		}
	}
}

func TestPressureSignalsClearWhenUnavailableOrDisabled(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cfg    AdmissionConfig
		limit  float64
		memory uint64
	}{
		{"disabled", AdmissionConfig{}, 1, 100},
		{"unlimited", AdmissionConfig{
			CPUPressureThresholdPercent: 80, MemoryPressureThresholdPercent: 80,
			CPUThresholdPercent: 90, MemoryThresholdPercent: 90,
		}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &sequenceCarrier{snapshots: []CarrierSnapshot{
				{CPULimitCores: tc.limit, MemoryLimitBytes: tc.memory, MemoryCurrentBytes: 100},
				{CPULimitCores: tc.limit, CPUUsage: time.Second, MemoryLimitBytes: tc.memory, MemoryCurrentBytes: 100},
			}}
			a := newPressureAdmission(c, tc.cfg)
			now := time.Unix(100, 0)
			a.sample(now)
			a.sample(now.Add(time.Second))
			r := a.Report()
			if r.CPUPressure || r.MemoryPressure || !r.Accepting {
				t.Fatalf("report=%+v", r)
			}
		})
	}
	c := &sequenceCarrier{snapshots: []CarrierSnapshot{{MemoryLimitBytes: 100, MemoryCurrentBytes: 95}}}
	a := newPressureAdmission(c, AdmissionConfig{MemoryPressureThresholdPercent: 80, MemoryThresholdPercent: 90})
	now := time.Unix(100, 0)
	a.sample(now)
	if !a.Report().MemoryPressure {
		t.Fatal("expected pressure")
	}
	c.err = errors.New("sample unavailable")
	a.sample(now.Add(time.Second))
	if r := a.Report(); r.CPUPressure || r.MemoryPressure || !r.Accepting {
		t.Fatalf("stale pressure=%+v", r)
	}
}
