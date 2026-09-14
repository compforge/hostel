package manager

import (
	"maps"
	"slices"

	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/configuration"
)

// WithConfiguration registers deployment-owned file sources before any Bed starts.
func WithConfiguration(c configuration.Config) ManagerOption {
	return func(m *Manager) {
		m.configurationConfig = configuration.Config{Sources: maps.Clone(c.Sources)}
	}
}

func (o CreateOptions) configuration() model.Configuration {
	return model.Configuration{Env: o.Env, EnvFiles: o.EnvFiles, EnvFrom: o.EnvFrom, EnvValueFrom: o.EnvValueFrom}
}

func checkBedConfiguration(o CreateOptions, s model.Spec) error {
	if o.lookup {
		return nil
	}
	if !maps.Equal(o.Env, s.Env) || !maps.Equal(o.EnvFiles, s.EnvFiles) ||
		!slices.Equal(o.EnvFrom, s.EnvFrom) || !maps.Equal(o.EnvValueFrom, s.EnvValueFrom) {
		return ErrEnvConflict
	}
	return nil
}

func (m *Manager) ConfigurationStatus() configuration.Report { return m.configurations.Status() }
