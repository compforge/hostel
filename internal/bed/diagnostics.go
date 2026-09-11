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

package bed

import (
	"time"

	"github.com/qiankunli/hostel/internal/isolation"
	"github.com/qiankunli/hostel/internal/network"
	"github.com/qiankunli/hostel/internal/privilege"
	"github.com/qiankunli/hostel/internal/resource"
	"github.com/qiankunli/hostel/internal/store"
)

// Diagnostics is the versioned operator view of one Hostel instance. Each
// section has one owning component; the HTTP layer only serializes this value.
type Diagnostics struct {
	SchemaVersion int                  `json:"schema_version"`
	Environment   EnvironmentReport    `json:"environment"`
	Isolation     IsolationDiagnostics `json:"isolation"`
	Privilege     privilege.Report     `json:"privilege"`
	Network       network.Report       `json:"network"`
	Executor      ExecutorDiagnostics  `json:"executor"`
	Store         store.Report         `json:"store"`
	Resource      ResourceDiagnostics  `json:"resource"`
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

type IsolationDiagnostics struct {
	Requested     string                           `json:"requested"`
	Effective     string                           `json:"effective"`
	Ceiling       string                           `json:"ceiling"`
	Mechanism     string                           `json:"mechanism"`
	WorkspaceView isolation.WorkspaceViewReport    `json:"workspace_view"`
	System        isolation.SystemFacts            `json:"system"`
	Probes        map[string]isolation.ProbeReport `json:"probes"`
}

type ExecutorDiagnostics struct {
	Backend string `json:"backend"`
}

type ResourceDiagnostics struct {
	Accounting resource.Report          `json:"accounting"`
	Admission  resource.AdmissionReport `json:"admission"`
}

// Diagnostics returns component snapshots only. Isolation and privilege facts
// were captured at startup; this method performs no host probes or remote I/O.
func (m *Manager) Diagnostics() Diagnostics {
	m.diagnosticsMu.RLock()
	environment := m.environment
	m.diagnosticsMu.RUnlock()
	view := IsolationDiagnostics{
		Effective:     m.iso.Level().String(),
		Mechanism:     m.iso.Name(),
		WorkspaceView: isolation.WorkspaceViewReport{Mode: "carrier", Available: true},
		Probes:        map[string]isolation.ProbeReport{},
	}
	if m.iso.WorkspaceMounted() {
		view.WorkspaceView.Mode = "mount"
	}
	if report, ok := m.iso.(isolation.Report); ok {
		details := report.Diagnostics()
		view.Requested = report.Requested().String()
		view.Effective = report.Effective().String()
		view.Ceiling = report.Ceiling().String()
		view.Mechanism = report.Mechanism()
		view.WorkspaceView = report.WorkspaceView()
		view.System = details.System
		view.Probes = details.Probes
	}
	amenities := make(map[string]string)
	for _, item := range m.amenities.List() {
		amenities[item.Name()] = item.State()
	}
	return Diagnostics{
		SchemaVersion: 1,
		Environment:   environment,
		Isolation:     view,
		Privilege:     m.privilegeReport,
		Network:       m.network.Report(),
		Executor:      ExecutorDiagnostics{Backend: m.executorFactory.Backend()},
		Store:         m.store.Report(),
		Resource: ResourceDiagnostics{
			Accounting: m.resources.Report(),
			Admission:  m.admission.Report(),
		},
		Amenities: amenities,
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
