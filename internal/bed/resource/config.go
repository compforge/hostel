package resource

import "github.com/qiankunli/hostel/internal/bed/tool"

type Config struct {
	Cgroup    tool.Policy
	Admission AdmissionConfig
}
type Options struct {
	Cgroup                 *tool.Policy
	CPUThresholdPercent    *int
	MemoryThresholdPercent *int
}

func (c Config) Validate() error { return c.Cgroup.Validate() }
