package resource

import "github.com/qiankunli/hostel/internal/bed/tool"

type Config struct {
	Cgroup    tool.Policy
	Admission AdmissionConfig
}
type Options struct {
	Cgroup                         *tool.Policy
	CPUPressureThresholdPercent    *int
	MemoryPressureThresholdPercent *int
	CPUThresholdPercent            *int
	MemoryThresholdPercent         *int
}

func (c Config) Validate() error {
	if err := c.Cgroup.Validate(); err != nil {
		return err
	}
	return c.Admission.Validate()
}
