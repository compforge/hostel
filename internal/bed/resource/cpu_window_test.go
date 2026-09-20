package resource

import (
	"math"
	"testing"
	"time"
)

func TestCPUUsageWindow(t *testing.T) {
	for _, tc := range []struct {
		name         string
		times, usage []float64
		limit, want  float64
	}{
		{"startup uses actual duration", []float64{0, 2}, []float64{0, 1}, 1, 50},
		{"one second spike is smoothed", []float64{0, 9, 10}, []float64{0, 0, 1}, 1, 10},
		{"sustained saturation", []float64{0, 5, 10}, []float64{0, 5, 10}, 1, 100},
		{"expired spike leaves window", []float64{0, 1, 10, 11}, []float64{0, 1, 1, 1}, 1, 0},
		{"irregular boundary interpolates", []float64{0, 4, 12}, []float64{0, 4, 4}, 1, 20},
		{"fractional cpu quota", []float64{0, 10}, []float64{0, 4}, 0.5, 80},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w cpuUsageWindow
			var got float64
			var ok bool
			for i, seconds := range tc.times {
				got, ok = w.add(time.Unix(100, 0).Add(time.Duration(seconds*float64(time.Second))), time.Duration(tc.usage[i]*float64(time.Second)), tc.limit)
			}
			if !ok || math.Abs(got-tc.want) > 1e-8 {
				t.Fatalf("got %v available=%v, want %v", got, ok, tc.want)
			}
		})
	}
}

func TestCPUUsageWindowResetsIncomparableHistory(t *testing.T) {
	for _, tc := range []struct {
		name      string
		at, usage time.Duration
		limit     float64
	}{
		{"counter reset", 2 * time.Second, 0, 1},
		{"quota change", 2 * time.Second, 2 * time.Second, 2},
		{"unlimited", 2 * time.Second, 2 * time.Second, 0},
		{"clock regression", 0, time.Second, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w cpuUsageWindow
			now := time.Unix(100, 0)
			w.add(now, 0, 1)
			w.add(now.Add(time.Second), time.Second, 1)
			if _, ok := w.add(now.Add(tc.at), tc.usage, tc.limit); ok {
				t.Fatal("incomparable samples reused")
			}
		})
	}
}

func TestAdmissionSharesSmoothedCPUWithReport(t *testing.T) {
	c := &sequenceCarrier{snapshots: []CarrierSnapshot{
		{CPULimitCores: 1}, {CPULimitCores: 1}, {CPULimitCores: 1, CPUUsage: time.Second},
		{CPULimitCores: 1, CPUUsage: 11 * time.Second},
		{CPULimitCores: 1, CPUUsage: 11 * time.Second},
	}}
	a := newPressureAdmission(c, AdmissionConfig{CPUPressureThresholdPercent: 80, CPUThresholdPercent: 90})
	now := time.Unix(100, 0)
	a.sample(now)
	a.sample(now.Add(9 * time.Second))
	a.sample(now.Add(10 * time.Second))
	if r := a.Report(); r.CPUUsagePercent != 10 || r.CPUPressure || !r.Accepting || !a.Check().Allowed {
		t.Fatalf("spike: %+v", r)
	}
	a.sample(now.Add(20 * time.Second))
	if r := a.Report(); r.CPUUsagePercent != 100 || !r.CPUPressure || r.Accepting || a.Check().Allowed {
		t.Fatalf("sustained: %+v", r)
	}
	a.sample(now.Add(30 * time.Second))
	if r := a.Report(); r.CPUUsagePercent != 0 || r.CPUPressure || !r.Accepting {
		t.Fatalf("recovery: %+v", r)
	}
}
