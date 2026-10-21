package bed

import (
	"maps"
	"slices"

	"github.com/qiankunli/hostel/protocol"
)

type PillowSpec = protocol.PillowSpec
type PillowHTTPSpec = protocol.PillowHTTPSpec
type Authentication = protocol.Authentication
type TokenSource = protocol.TokenSource

const (
	TokenSourceGenerated   = protocol.TokenSourceGenerated
	TokenSourceEnvironment = protocol.TokenSourceEnvironment
)

func ClonePillows(in []PillowSpec) []PillowSpec {
	out := slices.Clone(in)
	for i, s := range in {
		s.Mounts = slices.Clone(s.Mounts)
		s.Command = slices.Clone(s.Command)
		s.Args = slices.Clone(s.Args)
		s.Env = maps.Clone(s.Env)
		s.EnvFiles = maps.Clone(s.EnvFiles)
		s.EnvFrom = slices.Clone(s.EnvFrom)
		s.EnvValueFrom = maps.Clone(s.EnvValueFrom)
		if s.HTTP != nil {
			http := *s.HTTP
			if http.Authentication != nil {
				auth := *http.Authentication
				http.Authentication = &auth
			}
			s.HTTP = &http
		}
		out[i] = s
	}
	return out
}

// DefaultPillow resolves omission only; published declarations are normalized by Pillow.
// +spec=`Pillow order is semantic: the first declaration is the default. Names are stable identities.`
func (s Spec) DefaultPillow() PillowSpec {
	if len(s.Pillows) == 0 {
		return PillowSpec{Name: "main", Workdir: "/workspace"}
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
