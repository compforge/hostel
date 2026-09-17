package bed

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// PortMappingSpec is a named Bed network demand. Host ports are allocations,
// never caller-owned or durable configuration. BedPort is a preference unless
// RequireBedPort is set; shared networks otherwise use the host port pool.
type PortMappingSpec struct {
	Name           string `json:"name"`
	Protocol       string `json:"protocol,omitempty"`
	BedPort        int    `json:"bed_port,omitempty"`
	RequireBedPort bool   `json:"require_bed_port,omitempty"`
	Publish        bool   `json:"publish"`
}

var portMappingName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

func NormalizePortMappings(specs []PortMappingSpec) ([]PortMappingSpec, error) {
	out := slices.Clone(specs)
	names := make(map[string]bool)
	required := make(map[int]bool)
	for i := range out {
		s := &out[i]
		if !portMappingName.MatchString(s.Name) || names[s.Name] {
			return nil, fmt.Errorf("invalid or duplicate port mapping %q", s.Name)
		}
		names[s.Name] = true
		if s.Protocol == "" {
			s.Protocol = "tcp"
		}
		if s.Protocol != "tcp" {
			return nil, fmt.Errorf("port mapping %s: unsupported protocol %q", s.Name, s.Protocol)
		}
		if s.BedPort < 0 || s.BedPort > 65535 || (s.RequireBedPort && s.BedPort == 0) {
			return nil, fmt.Errorf("port mapping %s: invalid bed port", s.Name)
		}
		if s.RequireBedPort {
			if required[s.BedPort] {
				return nil, fmt.Errorf("required bed port %d is declared more than once", s.BedPort)
			}
			required[s.BedPort] = true
		}
	}
	slices.SortFunc(out, func(a, b PortMappingSpec) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}
