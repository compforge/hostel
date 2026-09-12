package resource

import "github.com/qiankunli/hostel/internal/feature"

type Config struct {
	Cgroup    feature.Policy
	Admission AdmissionConfig
}
type Options struct {
	Cgroup                 *feature.Policy
	CPUThresholdPercent    *int
	MemoryThresholdPercent *int
}

func (c Config) Validate() error { return c.Cgroup.Validate() }
