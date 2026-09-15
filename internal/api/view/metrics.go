package view

// SystemMetrics matches OpenSandbox execd's Metrics response exactly. In
// hostel, used CPU/memory are scoped to the selected bed while totals describe
// the carrier capacity shared by all beds.
type SystemMetrics struct {
	CPUCount    float64 `json:"cpu_count"`
	CPUUsedPct  float64 `json:"cpu_used_pct"`
	MemTotalMiB float64 `json:"mem_total_mib"`
	MemUsedMiB  float64 `json:"mem_used_mib"`
	Timestamp   int64   `json:"timestamp"`
}
