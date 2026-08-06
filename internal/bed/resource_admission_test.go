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
	"errors"
	"testing"

	"github.com/qiankunli/hostel/internal/isolation"
	"github.com/qiankunli/hostel/internal/resource"
)

type mutableAdmission struct {
	decision resource.AdmissionDecision
}

func (a *mutableAdmission) Check() resource.AdmissionDecision { return a.decision }
func (a *mutableAdmission) Report() resource.AdmissionReport {
	return resource.AdmissionReport{Enabled: true, Available: true, Accepting: a.decision.Allowed}
}

func TestResourceAdmissionChecksSyncedIdleAndNewBeds(t *testing.T) {
	m := newTestManager(t)
	a, _ := m.Ensure("a")
	b, _ := m.Ensure("b")
	defaultBed, _ := m.Ensure("")
	admission := &mutableAdmission{decision: resource.AdmissionDecision{Allowed: true}}
	m.SetResourceAdmission(admission)

	finishA, err := m.BeginOperation(a, OpExec, 0)
	if err != nil {
		t.Fatalf("activate a: %v", err)
	}
	admission.decision = resource.AdmissionDecision{Allowed: false, Reason: "carrier CPU usage 95.0%"}
	finishA2, err := m.BeginOperation(a, OpFile, 0)
	if err != nil {
		t.Fatalf("existing active bed was rejected: %v", err)
	}
	if _, err := m.BeginOperation(b, OpExec, 0); !errors.Is(err, ErrResourcePressure) {
		t.Fatalf("activate synced idle b: want ErrResourcePressure, got %v", err)
	}
	if _, err := m.Ensure("new"); !errors.Is(err, ErrResourcePressure) {
		t.Fatalf("admit new resident: want ErrResourcePressure, got %v", err)
	}
	finishDefault, err := m.BeginOperation(defaultBed, OpExec, 0)
	if err != nil {
		t.Fatalf("default bed should bypass resource admission: %v", err)
	}

	finishDefault()
	finishA2()
	finishA()
}

func TestResourceAdmissionAllowsUnsyncedIdleBed(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 3, newFakeStore())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	b, _ := m.Ensure("dirty")
	finishInitial, err := m.BeginOperation(b, OpExec, 0)
	if err != nil {
		t.Fatalf("initial operation: %v", err)
	}
	finishInitial()
	m.SetResourceAdmission(&mutableAdmission{decision: resource.AdmissionDecision{
		Allowed: false,
		Reason:  "carrier CPU usage 95.0%",
	}})
	finish, err := m.BeginOperation(b, OpExec, 0)
	if err != nil {
		t.Fatalf("unsynced idle bed must retain its carrier: %v", err)
	}
	finish()
}
