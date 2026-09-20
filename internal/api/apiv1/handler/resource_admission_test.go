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

package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/resource"
)

type rejectingResourceAdmission struct{}

func (rejectingResourceAdmission) Check() resource.AdmissionDecision {
	return resource.AdmissionDecision{Allowed: false, Reason: "carrier memory usage 95.0% reached 90% admission threshold"}
}

func (rejectingResourceAdmission) Report() resource.AdmissionReport {
	return resource.AdmissionReport{
		Enabled: true, Available: true, Accepting: false,
		MemoryThresholdPercent: 90, MemoryCurrentBytes: 950, MemoryLimitBytes: 1000,
		MemoryUsagePercent: 95, MemoryAvailable: true, MemoryPressure: true,
		Reason:    "carrier memory usage 95.0% reached 90% admission threshold",
		SampledAt: time.Unix(100, 0),
	}
}

func TestResourcePressureBackpressureAndReporting(t *testing.T) {
	s := newTestServer(t)
	s.mgr.SetResourceAdmission(rejectingResourceAdmission{})

	rec := do(t, s, http.MethodGet, "/files/info?path=/workspace", nil, map[string]string{BedHeader: "new-tenant"})
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "RESOURCE_PRESSURE") {
		t.Fatalf("resource pressure = %d %s", rec.Code, rec.Body.String())
	}
	// The compatibility default bed remains available even under pressure.
	if rec = do(t, s, http.MethodGet, "/files/info?path=/workspace", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("default bed under pressure = %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, s, http.MethodGet, "/healthz", nil, nil)
	var health struct {
		ResourceAdmission struct {
			Enabled            bool    `json:"enabled"`
			Available          bool    `json:"available"`
			Accepting          bool    `json:"accepting"`
			MemoryUsagePercent float64 `json:"memory_usage_percent"`
			MemoryLimitBytes   uint64  `json:"memory_limit_bytes"`
		} `json:"resource_admission"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatalf("decode healthz: %v", err)
	}
	if !health.ResourceAdmission.Enabled || !health.ResourceAdmission.Available ||
		health.ResourceAdmission.Accepting || health.ResourceAdmission.MemoryUsagePercent != 95 ||
		health.ResourceAdmission.MemoryLimitBytes != 1000 {
		t.Fatalf("resource admission health = %+v", health.ResourceAdmission)
	}
}

func TestStatusReportsResourcePressure(t *testing.T) {
	s := newTestServer(t)
	s.mgr.SetResourceAdmission(rejectingResourceAdmission{})
	for _, path := range []string{"/v1/status", "/v1/beds"} {
		rec := do(t, s, http.MethodGet, path, nil, nil)
		var got struct {
			Instance struct {
				CPU       *bool                    `json:"cpu_pressure"`
				Memory    *bool                    `json:"memory_pressure"`
				Admission resource.AdmissionReport `json:"resource_admission"`
			} `json:"instance"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if rec.Code != 200 || got.Instance.CPU == nil || *got.Instance.CPU || got.Instance.Memory == nil || !*got.Instance.Memory || got.Instance.Admission.Accepting {
			t.Fatalf("%s: %s", path, rec.Body.String())
		}
	}
}
