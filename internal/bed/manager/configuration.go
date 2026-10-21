package manager

import (
	"maps"

	"github.com/qiankunli/hostel/internal/bed/configuration"
)

// WithConfiguration registers deployment-owned file sources before any Bed starts.
func WithConfiguration(c configuration.Config) ManagerOption {
	return func(m *Manager) {
		m.configurationConfig = configuration.Config{Sources: maps.Clone(c.Sources)}
	}
}

func (m *Manager) ConfigurationStatus() configuration.Report { return m.configurations.Status() }
