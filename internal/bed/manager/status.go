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

package manager

import (
	"time"

	model "github.com/qiankunli/hostel/internal/bed"

	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/filesystem"
	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/bed/privilege"
	"github.com/qiankunli/hostel/internal/bed/resource"
	"github.com/qiankunli/hostel/internal/bed/store"
)

// Status aggregates Bed inventory and domain-owned Component Status. Amenity
// facilities are peers and are composed separately by the Hostel instance.
type Status struct {
	InventoryStatus
	LocalCleanups []LocalCleanupReport `json:"local_cleanups"`
	Environment   EnvironmentReport    `json:"environment"`
	Components    struct {
		Filesystem filesystem.Status `json:"filesystem"`
		Privilege  privilege.Status  `json:"privilege"`
		Network    network.Status    `json:"network"`
		Executor   executor.Status   `json:"executor"`
		Store      store.Status      `json:"store"`
		Resource   resource.Status   `json:"resource"`
	} `json:"components"`
}

const (
	EnvironmentProbeNotRun  = "not_run"
	EnvironmentProbeRunning = "running"
	EnvironmentProbePassed  = "passed"
	EnvironmentProbeFailed  = "failed"
)

// EnvironmentReport records the real composition probe that exercises BedFS,
// networking, credentials and the Executor through normal Bed entry points.
// It is distinct from each component's startup prerequisites.
type EnvironmentReport struct {
	ProbeStatus string    `json:"probe_status"`
	StartedAt   time.Time `json:"started_at,omitzero"`
	FinishedAt  time.Time `json:"finished_at,omitzero"`
	DurationMS  int64     `json:"duration_ms"`
	Error       string    `json:"error,omitempty"`
}

// Status returns component snapshots only. Isolation and privilege facts
// were captured at startup; this method performs no host probes or remote I/O.
func (m *Manager) Status() Status {
	m.diagnosticsMu.RLock()
	environment := m.environment
	m.diagnosticsMu.RUnlock()
	report := Status{InventoryStatus: m.InventoryStatus(), LocalCleanups: m.localCleanupReports(), Environment: environment}
	report.Components.Filesystem = m.files.Status()
	report.Components.Privilege = m.privileges.Status()
	report.Components.Network = m.network.Status()
	report.Components.Executor = m.executorManager.Status()
	report.Components.Store = m.store.Status()
	report.Components.Resource = m.resourceManager.Status()
	return report
}

func (m *Manager) startEnvironmentProbe(started time.Time) {
	m.diagnosticsMu.Lock()
	m.environment = EnvironmentReport{ProbeStatus: EnvironmentProbeRunning, StartedAt: started}
	m.diagnosticsMu.Unlock()
}

func (m *Manager) finishEnvironmentProbe(started time.Time, err error) {
	finished := time.Now()
	report := EnvironmentReport{
		ProbeStatus: EnvironmentProbePassed,
		StartedAt:   started,
		FinishedAt:  finished,
		DurationMS:  finished.Sub(started).Milliseconds(),
	}
	if err != nil {
		report.ProbeStatus = EnvironmentProbeFailed
		report.Error = err.Error()
	}
	m.diagnosticsMu.Lock()
	m.environment = report
	m.diagnosticsMu.Unlock()
}

// BedModel returns an observation handle, never an admitted operation. It also
// covers initializing and retiring identities so detail queries keep one shape.
func (m *Manager) BedModel(name string) *model.Bed {
	if name == "" {
		name = m.defaultBed
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if b := m.beds[name]; b != nil {
		return b.Bed
	}
	if b := m.retirements[name]; b != nil {
		return b.Bed
	}
	if init := m.initializations[name]; init != nil {
		return init.model
	}
	return nil
}
