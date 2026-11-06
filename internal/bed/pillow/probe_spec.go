package pillow

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/qiankunli/hostel/internal/bed"
)

func normalizePortsAndProbe(s *bed.PillowSpec) error {
	for i := range s.Ports {
		p := &s.Ports[i]
		if !identifier.MatchString(p.Name) {
			return fmt.Errorf("invalid port %q", p.Name)
		}
		if strings.Trim(p.Name, "0123456789") == "" {
			return fmt.Errorf("port name %q must not be numeric", p.Name)
		}
		if p.Protocol == "" {
			p.Protocol = "TCP"
		}
		if p.Protocol != "TCP" {
			return fmt.Errorf("port %s: only TCP is supported", p.Name)
		}
		if p.Dynamic {
			if p.ContainerPort != 0 {
				return fmt.Errorf("port %s: dynamic and containerPort are mutually exclusive", p.Name)
			}
		} else {
			if p.ContainerPort < 1 || p.ContainerPort > 65535 {
				return fmt.Errorf("port %s: specify containerPort in 1..65535 or dynamic=true", p.Name)
			}

		}
	}
	probe := s.ReadinessProbe
	if probe == nil {
		return nil
	}
	if (probe.HTTPGet == nil) == (probe.TCPSocket == nil) {
		return fmt.Errorf("readinessProbe requires exactly one of httpGet or tcpSocket")
	}
	if probe.InitialDelaySeconds < 0 || probe.TimeoutSeconds < 0 || probe.PeriodSeconds < 0 || probe.SuccessThreshold < 0 || probe.FailureThreshold < 0 {
		return fmt.Errorf("readinessProbe timing and thresholds must not be negative")
	}
	// Bound conversion to time.Duration and avoid accidental unbounded control loops.
	if probe.InitialDelaySeconds > 86400 || probe.TimeoutSeconds > 3600 || probe.PeriodSeconds > 3600 || probe.SuccessThreshold > 1000 || probe.FailureThreshold > 1000 {
		return fmt.Errorf("readinessProbe timing or threshold exceeds supported limit")
	}
	if probe.TimeoutSeconds == 0 {
		probe.TimeoutSeconds = 1
	}
	if probe.PeriodSeconds == 0 {
		probe.PeriodSeconds = 10
	}
	if probe.SuccessThreshold == 0 {
		probe.SuccessThreshold = 1
	}
	if probe.FailureThreshold == 0 {
		probe.FailureThreshold = 3
	}
	if probe.HTTPGet != nil {
		if probe.HTTPGet.Path == "" {
			probe.HTTPGet.Path = "/"
		}
		u, err := url.ParseRequestURI(probe.HTTPGet.Path)
		if err != nil || !strings.HasPrefix(probe.HTTPGet.Path, "/") || strings.HasPrefix(probe.HTTPGet.Path, "//") || u.IsAbs() {
			return fmt.Errorf("httpGet.path must be an absolute request path")
		}
	}
	name := probePortName(*s)
	if name == "" {
		return fmt.Errorf("readinessProbe.port must reference a port declared by this Pillow")
	}
	// Pin one representation so numeric and named references compare identically after JSON recovery.
	if probe.HTTPGet != nil {
		probe.HTTPGet.Port = bed.ProbePort(name)
	}
	if probe.TCPSocket != nil {
		probe.TCPSocket.Port = bed.ProbePort(name)
	}
	return nil
}

func probePortName(s bed.PillowSpec) string {
	if s.ReadinessProbe == nil {
		return ""
	}
	var reference bed.ProbePort
	if s.ReadinessProbe.HTTPGet != nil {
		reference = s.ReadinessProbe.HTTPGet.Port
	}
	if s.ReadinessProbe.TCPSocket != nil {
		reference = s.ReadinessProbe.TCPSocket.Port
	}
	number, numeric := strconv.Atoi(string(reference))
	for _, p := range s.Ports {
		if p.Name == string(reference) || (numeric == nil && !p.Dynamic && p.ContainerPort == number) {
			return p.Name
		}
	}
	return ""
}

// Port names are Bed-wide discovery identities; fixed listeners share a Bed network.
func validateBedPorts(specs []bed.PillowSpec) error {
	names := make(map[string]string)
	numbers := make(map[int]string)
	for _, s := range specs {
		for _, p := range s.Ports {
			if owner, exists := names[p.Name]; exists {
				return fmt.Errorf("port %s is declared by both pillows %s and %s", p.Name, owner, s.Name)
			}
			names[p.Name] = s.Name
			if !p.Dynamic {
				if owner, exists := numbers[p.ContainerPort]; exists {
					return fmt.Errorf("containerPort %d is declared by both pillows %s and %s", p.ContainerPort, owner, s.Name)
				}
				numbers[p.ContainerPort] = s.Name
			}
		}
	}
	return nil
}
