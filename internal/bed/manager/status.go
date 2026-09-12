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

	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/filesystem"
	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/bed/privilege"
	"github.com/qiankunli/hostel/internal/bed/resource"
	"github.com/qiankunli/hostel/internal/bed/store"
)

// Status is the versioned operator view of one Hostel instance. Each
// section has one owning component; the HTTP layer only serializes this value.
type Status struct {
	InventoryStatus
	LocalCleanups []LocalCleanupReport `json:"local_cleanups"`
	SchemaVersion int                  `json:"schema_version"`
	Environment   EnvironmentReport    `json:"environment"`
	Isolation     filesystem.Status    `json:"isolation"`
	Privilege     privilege.Status     `json:"privilege"`
	Network       network.Status       `json:"network"`
	Executor      executor.Status      `json:"executor"`
	Store         store.Status         `json:"store"`
	Resource      resource.Status      `json:"resource"`
	Amenities     map[string]string    `json:"amenities"`
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
	return Status{
		InventoryStatus: m.InventoryStatus(),
		LocalCleanups:   m.localCleanupReports(),
		SchemaVersion:   1,
		Environment:     environment,
		Isolation:       m.files.Status(),
		Privilege:       m.privileges.Status(),
		Network:         m.network.Status(),
		Executor:        m.executorManager.Status(),
		Store:           m.store.Status(),
		Resource:        m.resourceManager.Status(),
		Amenities:       m.amenities.Status(),
	}
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
