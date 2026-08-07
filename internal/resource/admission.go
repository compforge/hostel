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
	"fmt"
	"strings"
	"sync"
	"time"
)

const defaultAdmissionSampleInterval = time.Second

// AdmissionConfig controls carrier-pressure admission. A zero threshold
// disables that dimension; values are percentages in [0, 100].
type AdmissionConfig struct {
	CPUThresholdPercent    int
	MemoryThresholdPercent int
	SampleInterval         time.Duration
}

// AdmissionDecision is the cached verdict used on an idle-to-active bed
// transition. Check never performs cgroup I/O on the request path.
type AdmissionDecision struct {
	Allowed bool
	Reason  string
}

// AdmissionReport exposes the latest sampled facts and verdict. Availability
// is true when at least one configured dimension has a finite cgroup limit and
// a usable sample.
type AdmissionReport struct {
	Enabled                bool
	Available              bool
	Accepting              bool
	Reason                 string
	CPUThresholdPercent    int
	MemoryThresholdPercent int
	CPULimitCores          float64
	CPUUsagePercent        float64
	CPUAvailable           bool
	MemoryCurrentBytes     uint64
	MemoryLimitBytes       uint64
	MemoryUsagePercent     float64
	MemoryAvailable        bool
	SampledAt              time.Time
}

// Admitter answers whether carrier pressure allows another idle tenant bed to
// become active. It deliberately does not count beds; Manager composes this
// signal with max-pinned-beds under its lifecycle lock.
type Admitter interface {
	Check() AdmissionDecision
	Report() AdmissionReport
}

// NewAdmission starts a read-only cgroup sampler. Sampling is decoupled from
// requests so CPU admission does not add a measurement delay to bed startup.
func NewAdmission(ctx context.Context, carrier Carrier, cfg AdmissionConfig) (Admitter, error) {
	if err := validateThreshold("CPU", cfg.CPUThresholdPercent); err != nil {
		return nil, err
	}
	if err := validateThreshold("memory", cfg.MemoryThresholdPercent); err != nil {
		return nil, err
	}
	if cfg.SampleInterval <= 0 {
		cfg.SampleInterval = defaultAdmissionSampleInterval
	}
	a := newPressureAdmission(carrier, cfg)
	if !a.report.Enabled {
		return a, nil
	}
	a.sample(time.Now())
	go a.run(ctx)
	return a, nil
}

func validateThreshold(name string, threshold int) error {
	if threshold < 0 || threshold > 100 {
		return fmt.Errorf("resource admission: %s threshold must be between 0 and 100: %d", name, threshold)
	}
	return nil
}

// NoopAdmission is the startup default before cmd/hostel installs the carrier
// sampler. It always admits and reports why pressure admission is disabled.
func NoopAdmission(reason string) Admitter {
	return &pressureAdmission{
		decision: AdmissionDecision{Allowed: true},
		report: AdmissionReport{
			Accepting: true,
			Reason:    reason,
		},
	}
}

type pressureAdmission struct {
	carrier Carrier
	cfg     AdmissionConfig

	mu          sync.RWMutex
	previous    CarrierSnapshot
	previousAt  time.Time
	hasPrevious bool
	decision    AdmissionDecision
	report      AdmissionReport
}

func newPressureAdmission(carrier Carrier, cfg AdmissionConfig) *pressureAdmission {
	enabled := cfg.CPUThresholdPercent > 0 || cfg.MemoryThresholdPercent > 0
	reason := ""
	if !enabled {
		reason = "resource admission disabled"
	}
	return &pressureAdmission{
		carrier:  carrier,
		cfg:      cfg,
		decision: AdmissionDecision{Allowed: true},
		report: AdmissionReport{
			Enabled:                enabled,
			Accepting:              true,
			Reason:                 reason,
			CPUThresholdPercent:    cfg.CPUThresholdPercent,
			MemoryThresholdPercent: cfg.MemoryThresholdPercent,
		},
	}
}

func (a *pressureAdmission) run(ctx context.Context) {
	ticker := time.NewTicker(a.cfg.SampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			a.sample(now)
		}
	}
}

func (a *pressureAdmission) sample(now time.Time) {
	snapshot, err := a.carrier.Snapshot()
	if err != nil {
		a.mu.Lock()
		a.decision = AdmissionDecision{Allowed: true}
		a.report = AdmissionReport{
			Enabled:                true,
			Accepting:              true,
			Reason:                 err.Error(),
			CPUThresholdPercent:    a.cfg.CPUThresholdPercent,
			MemoryThresholdPercent: a.cfg.MemoryThresholdPercent,
			SampledAt:              now,
		}
		a.mu.Unlock()
		return
	}

	report := AdmissionReport{
		Enabled:                true,
		Accepting:              true,
		CPUThresholdPercent:    a.cfg.CPUThresholdPercent,
		MemoryThresholdPercent: a.cfg.MemoryThresholdPercent,
		CPULimitCores:          snapshot.CPULimitCores,
		MemoryCurrentBytes:     snapshot.MemoryCurrentBytes,
		MemoryLimitBytes:       snapshot.MemoryLimitBytes,
		SampledAt:              now,
	}
	var rejected []string
	if a.cfg.MemoryThresholdPercent > 0 && snapshot.MemoryLimitBytes > 0 {
		report.MemoryAvailable = true
		report.MemoryUsagePercent = float64(snapshot.MemoryCurrentBytes) / float64(snapshot.MemoryLimitBytes) * 100
		if report.MemoryUsagePercent >= float64(a.cfg.MemoryThresholdPercent) {
			rejected = append(rejected, fmt.Sprintf("carrier memory usage %.1f%% reached %d%% admission threshold",
				report.MemoryUsagePercent, a.cfg.MemoryThresholdPercent))
		}
	}

	a.mu.Lock()
	if a.cfg.CPUThresholdPercent > 0 && snapshot.CPULimitCores > 0 && a.hasPrevious &&
		snapshot.CPUUsage >= a.previous.CPUUsage && now.After(a.previousAt) {
		report.CPUAvailable = true
		used := snapshot.CPUUsage - a.previous.CPUUsage
		elapsed := now.Sub(a.previousAt)
		report.CPUUsagePercent = float64(used) / float64(elapsed) / snapshot.CPULimitCores * 100
		if report.CPUUsagePercent >= float64(a.cfg.CPUThresholdPercent) {
			rejected = append(rejected, fmt.Sprintf("carrier CPU usage %.1f%% reached %d%% admission threshold",
				report.CPUUsagePercent, a.cfg.CPUThresholdPercent))
		}
	}
	a.previous = snapshot
	a.previousAt = now
	a.hasPrevious = true
	report.Available = report.CPUAvailable || report.MemoryAvailable
	if !report.Available {
		report.Reason = unavailableReason(a.cfg, snapshot)
	}
	if len(rejected) > 0 {
		report.Accepting = false
		report.Reason = strings.Join(rejected, "; ")
		a.decision = AdmissionDecision{Allowed: false, Reason: report.Reason}
	} else {
		a.decision = AdmissionDecision{Allowed: true}
	}
	a.report = report
	a.mu.Unlock()
}

func unavailableReason(cfg AdmissionConfig, snapshot CarrierSnapshot) string {
	if cfg.MemoryThresholdPercent > 0 && snapshot.MemoryLimitBytes > 0 {
		return ""
	}
	if cfg.CPUThresholdPercent > 0 && snapshot.CPULimitCores > 0 {
		return "waiting for CPU sampling window"
	}
	return "configured cgroup dimensions have no finite limits"
}

func (a *pressureAdmission) Check() AdmissionDecision {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.decision
}

func (a *pressureAdmission) Report() AdmissionReport {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.report
}
