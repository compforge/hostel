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
	"fmt"
	"math"
	"net/http"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/mem"

	"github.com/qiankunli/hostel/internal/bed"
)

// systemMetrics matches OpenSandbox execd's Metrics response exactly. In
// hostel, used CPU/memory are scoped to the selected bed while totals describe
// the carrier capacity shared by all beds.
type systemMetrics struct {
	CPUCount    float64 `json:"cpu_count"`
	CPUUsedPct  float64 `json:"cpu_used_pct"`
	MemTotalMiB float64 `json:"mem_total_mib"`
	MemUsedMiB  float64 `json:"mem_used_mib"`
	Timestamp   int64   `json:"timestamp"`
}

func readBedMetrics(
	ctx context.Context,
	mgr *bed.Manager,
	bedID string,
	interval time.Duration,
) (systemMetrics, error) {
	metrics := systemMetrics{
		CPUCount: float64(runtime.GOMAXPROCS(-1)),
	}

	vmStat, err := mem.VirtualMemory()
	if err != nil {
		return systemMetrics{}, fmt.Errorf("read memory capacity: %w", err)
	}
	metrics.MemTotalMiB = float64(vmStat.Total) / 1024 / 1024
	if mgr.ResourceReport().Available {
		before, err := mgr.ResourceUsage(bedID)
		if err != nil {
			return systemMetrics{}, fmt.Errorf("read bed CPU baseline: %w", err)
		}
		timer := time.NewTimer(interval)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return systemMetrics{}, ctx.Err()
		case <-timer.C:
		}
		after, err := mgr.ResourceUsage(bedID)
		if err != nil {
			return systemMetrics{}, fmt.Errorf("read bed resource usage: %w", err)
		}
		if after.CPUUsage >= before.CPUUsage && interval > 0 && metrics.CPUCount > 0 {
			metrics.CPUUsedPct = (after.CPUUsage - before.CPUUsage).Seconds() /
				interval.Seconds() / metrics.CPUCount * 100
			metrics.CPUUsedPct = math.Max(0, math.Min(100, metrics.CPUUsedPct))
		}
		metrics.MemUsedMiB = float64(after.MemoryCurrentBytes) / 1024 / 1024
	} else {
		// Non-Linux development hosts keep upstream execd's instance-level
		// behavior so the protocol remains useful while healthz reports that
		// exact per-bed accounting is unavailable.
		cpuPercent, err := cpu.Percent(interval, false)
		if err != nil {
			return systemMetrics{}, fmt.Errorf("read CPU usage: %w", err)
		}
		if len(cpuPercent) > 0 {
			metrics.CPUUsedPct = cpuPercent[0]
		}
		metrics.MemUsedMiB = float64(vmStat.Used) / 1024 / 1024
	}
	metrics.Timestamp = time.Now().UnixMilli()
	return metrics, nil
}

func (s *Server) getMetrics(c *gin.Context) {
	b := s.bedOf(c)
	if b == nil {
		return
	}
	metrics, err := readBedMetrics(
		c.Request.Context(), s.mgr, b.ID, s.metricSampleInterval,
	)
	if err != nil {
		runtimeError(c, "error reading runtime metrics: "+err.Error())
		return
	}
	c.JSON(http.StatusOK, metrics)
}

func (s *Server) watchMetrics(c *gin.Context) {
	b := s.bedOf(c)
	if b == nil {
		return
	}
	for key, value := range sseHeaders {
		c.Writer.Header().Set(key, value)
	}
	c.Writer.WriteHeader(http.StatusOK)
	flush(c.Writer)

	for {
		select {
		case <-c.Request.Context().Done():
			return
		default:
		}

		metrics, err := readBedMetrics(
			c.Request.Context(), s.mgr, b.ID, s.metricSampleInterval,
		)
		select {
		case <-c.Request.Context().Done():
			return
		default:
		}

		var payload []byte
		if err != nil {
			payload, _ = json.Marshal(map[string]string{"error": err.Error()})
		} else {
			payload, _ = json.Marshal(metrics)
		}
		if _, err := c.Writer.Write(append(payload, '\n')); err != nil {
			return
		}
		flush(c.Writer)
	}
}
