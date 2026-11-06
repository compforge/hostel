package pillow

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/network"
)

type portConflictError struct{ name string }

func (e *portConflictError) Error() string {
	return "pillow port occupied by another process: " + e.name
}

func retryPortConflict(err error, spec bed.PillowSpec) bool {
	var conflict *portConflictError
	if !errors.As(err, &conflict) {
		return false
	}
	for _, port := range spec.Ports {
		if port.Name == conflict.name {
			return port.Dynamic
		}
	}
	return false
}

func (m *Manager) reservePorts(g *group, r *record) ([]*network.PortMapping, error) {
	if len(r.spec.Ports) == 0 {
		return nil, nil
	}
	scope, host, err := g.runtime.Network()
	if err != nil {
		return nil, err
	}
	var mappings []*network.PortMapping
	for _, port := range r.spec.Ports {
		mapping, err := m.mappings.Reserve(g.bed, port.Name, r.spec.Name, scope, host)
		if err != nil {
			return mappings, err
		}
		mappings = append(mappings, mapping)
	}
	g.mu.Lock()
	r.mappings = mappings
	g.mu.Unlock()
	return mappings, nil
}

func (m *Manager) releasePorts(g *group, r *record, mappings []*network.PortMapping) {
	for _, mapping := range mappings {
		for {
			if err := mapping.Release(); err == nil {
				break
			}
			m.update(g, r, func(s *Status) { s.Reason = "PortCleanupPending" })
			time.Sleep(time.Second)
		}
	}
	g.mu.Lock()
	r.mappings = nil
	g.mu.Unlock()
}

// +spec=`Only command/args expand allocated addresses. Environment values, including application credentials, remain opaque.`
func portCommand(spec bed.PillowSpec, mappings []*network.PortMapping) []string {
	command := append(append([]string(nil), spec.Command...), spec.Args...)
	var pairs []string
	for _, mapping := range mappings {
		name := mapping.Status().Name
		pairs = append(pairs, "${PORT_"+name+"}", strconv.Itoa(mapping.Port()), "${LISTEN_ADDR_"+name+"}", mapping.ListenAddress())
	}
	if len(mappings) == 1 {
		pairs = append(pairs, "${PORT}", strconv.Itoa(mappings[0].Port()), "${LISTEN_ADDR}", mappings[0].ListenAddress())
	}
	replace := strings.NewReplacer(pairs...)
	for i, arg := range command {
		command[i] = replace.Replace(arg)
	}
	return command
}

func probeMapping(spec bed.PillowSpec, mappings []*network.PortMapping) (*network.PortMapping, error) {
	name := probePortName(spec)
	for _, mapping := range mappings {
		if mapping.Status().Name == name {
			return mapping, nil
		}
	}
	return nil, fmt.Errorf("pillow %s readiness port %s is not allocated", spec.Name, name)
}
