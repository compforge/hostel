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
	"github.com/qiankunli/hostel/internal/bed/configuration"

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
	Isolation     model.RoomStatus     `json:"isolation"`
	Combinations  []CombinationAttempt `json:"combinations,omitempty"`
	Instance      StatusInstance       `json:"instance"`
	LocalCleanups []LocalCleanupReport `json:"local_cleanups"`
	Environment   EnvironmentReport    `json:"environment"`
	Components    struct {
		Configuration configuration.Report `json:"configuration"`
		Filesystem    filesystem.Status    `json:"filesystem"`
		Privilege     privilege.Status     `json:"privilege"`
		Network       network.Status       `json:"network"`
		Executor      executor.Status      `json:"executor"`
		Store         store.Status         `json:"store"`
		Resource      resource.Status      `json:"resource"`
	} `json:"components"`
}

// StatusInstance adds Hostel/Carrier signals to the Bed-derived instance
// summary. Bed rows remain owned by GET /v1/beds.
type StatusInstance struct {
	InstanceStatus
	BedPressureThresholdPercent int  `json:"bed_pressure_threshold_percent"`
	BedPressure                 bool `json:"bed_pressure"`
	CPUPressure                 bool `json:"cpu_pressure"`
	MemoryPressure              bool `json:"memory_pressure"`
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
	inventory := m.InventoryStatus()
	resourceStatus := m.resourceManager.Status()
	report := Status{Instance: StatusInstance{InstanceStatus: inventory.Instance,
		BedPressureThresholdPercent: m.pressurePercent,
		BedPressure:                 m.bedPressureForCounts(int64(inventory.Instance.OccupiedBeds), int64(inventory.Instance.PinnedBeds)),
		CPUPressure:                 resourceStatus.Admission.CPUPressure, MemoryPressure: resourceStatus.Admission.MemoryPressure},
		LocalCleanups: m.localCleanupReports(), Environment: environment,
		Isolation: m.RoomStatus(), Combinations: append([]CombinationAttempt(nil), m.combinationAttempts...)}
	report.Components.Filesystem = m.files.Status()
	report.Components.Configuration = m.configurations.Status()
	report.Components.Privilege = m.privileges.Status()
	report.Components.Network = m.network.Status()
	report.Components.Executor = m.executorManager.Status()
	report.Components.Store = m.store.Status()
	report.Components.Resource = resourceStatus
	return report
}

func (m *Manager) RoomStatus() model.RoomStatus {
	requested := m.roomType
	if requested == "" {
		requested = m.iso.Level().Room()
	}
	identity := privilege.Shared
	if m.identitySelection != nil {
		identity = m.identitySelection.Effective
	}
	return model.SummarizeRoom(requested, map[string]model.Level{
		"filesystem": m.iso.Level(), "privilege": identity, "network": m.network.Status().Effective,
	})
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
