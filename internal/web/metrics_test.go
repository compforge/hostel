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

package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/resource"
)

type fakeResourceTracker struct {
	calls  int
	bedIDs []string
}

func (t *fakeResourceTracker) Report() resource.Report {
	return resource.Report{Backend: "fake", Available: true}
}

func (t *fakeResourceTracker) OpenGroup(string) (*os.File, error) { return nil, nil }

func (t *fakeResourceTracker) Usage(bedID string) (resource.Usage, error) {
	t.calls++
	t.bedIDs = append(t.bedIDs, bedID)
	return resource.Usage{
		CPUUsage:           time.Duration(t.calls-1) * 5 * time.Millisecond,
		MemoryCurrentBytes: 64 * 1024 * 1024,
	}, nil
}

func (t *fakeResourceTracker) Release(string) error { return nil }

func TestMetricsEndpoint(t *testing.T) {
	s := newTestServer(t)
	tracker := &fakeResourceTracker{}
	s.mgr.SetResourceTracker(tracker)
	s.metricSampleInterval = 10 * time.Millisecond
	started := time.Now().Add(-time.Second).UnixMilli()
	rec := do(t, s, http.MethodGet, "/metrics", nil, map[string]string{BedHeader: "metrics-bed"})
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics = %d %s", rec.Code, rec.Body.String())
	}

	var metrics systemMetrics
	if err := json.Unmarshal(rec.Body.Bytes(), &metrics); err != nil {
		t.Fatalf("decode /metrics: %v", err)
	}
	if metrics.CPUCount <= 0 {
		t.Fatalf("cpu_count = %v", metrics.CPUCount)
	}
	if metrics.CPUUsedPct < 0 || metrics.CPUUsedPct > 100 {
		t.Fatalf("cpu_used_pct = %v", metrics.CPUUsedPct)
	}
	if metrics.CPUUsedPct == 0 {
		t.Fatal("cpu_used_pct did not use the bed cgroup delta")
	}
	if metrics.MemTotalMiB <= 0 || metrics.MemUsedMiB < 0 ||
		metrics.MemUsedMiB > metrics.MemTotalMiB {
		t.Fatalf("memory metrics = total:%v used:%v", metrics.MemTotalMiB, metrics.MemUsedMiB)
	}
	if metrics.MemUsedMiB != 64 {
		t.Fatalf("mem_used_mib = %v, want bed cgroup's 64 MiB", metrics.MemUsedMiB)
	}
	if metrics.Timestamp < started || metrics.Timestamp > time.Now().UnixMilli() {
		t.Fatalf("timestamp = %d", metrics.Timestamp)
	}
	if len(tracker.bedIDs) != 2 ||
		tracker.bedIDs[0] != "metrics-bed" ||
		tracker.bedIDs[1] != "metrics-bed" {
		t.Fatalf("usage sampled for beds %v", tracker.bedIDs)
	}
}

func TestMetricsWatchHeaders(t *testing.T) {
	s := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/metrics/watch", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics/watch = %d", rec.Code)
	}
	for key, want := range sseHeaders {
		if got := rec.Header().Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}
