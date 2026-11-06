package bed

import (
	"maps"
	"slices"

	"github.com/qiankunli/hostel/protocol"
)

type PillowSpec = protocol.PillowSpec
type PillowPort = protocol.PillowPort
type Probe = protocol.Probe
type ProbePort = protocol.ProbePort
type HTTPGetAction = protocol.HTTPGetAction
type TCPSocketAction = protocol.TCPSocketAction

func ClonePillows(in []PillowSpec) []PillowSpec {
	out := slices.Clone(in)
	for i, s := range in {
		s.Ports = slices.Clone(s.Ports)
		if s.ReadinessProbe != nil {
			probe := *s.ReadinessProbe
			if probe.HTTPGet != nil {
				action := *probe.HTTPGet
				probe.HTTPGet = &action
			}
			if probe.TCPSocket != nil {
				action := *probe.TCPSocket
				probe.TCPSocket = &action
			}
			s.ReadinessProbe = &probe
		}
		s.Mounts = slices.Clone(s.Mounts)
		s.Command = slices.Clone(s.Command)
		s.Args = slices.Clone(s.Args)
		s.Env = maps.Clone(s.Env)
		s.EnvFiles = maps.Clone(s.EnvFiles)
		s.EnvFrom = slices.Clone(s.EnvFrom)
		s.EnvValueFrom = maps.Clone(s.EnvValueFrom)
		out[i] = s
	}
	return out
}

// DefaultPillow resolves omission only; published declarations are normalized by Pillow.
// +spec=`Pillow order is semantic: the first declaration is the default. Names are stable identities.`
func (s Spec) DefaultPillow() PillowSpec {
	if len(s.Pillows) == 0 {
		return PillowSpec{}
	}
	return s.Pillows[0]
}

func (s Spec) HasMainProcesses() bool {
	for _, p := range s.Pillows {
		if len(p.Command) > 0 {
			return true
		}
	}
	return false
}

// NetworkPorts derives network demands without duplicating Pillow declarations in durable state.
func (s Spec) NetworkPorts() []PillowPort {
	var out []PillowPort
	for _, pillow := range s.Pillows {
		out = append(out, pillow.Ports...)
	}
	return out
}
