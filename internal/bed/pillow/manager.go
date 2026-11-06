package pillow

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/configuration"
	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/protocol"

	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

type Launch struct {
	Name      string
	Command   []string
	Directory string
	Env       map[string]string
}
type Process interface {
	PID() int
	ExecutionID() string
	ExecutorID() string
	Done() <-chan struct{}
	Outcome() executor.ProcessOutcome
	Stop(context.Context, time.Duration) error
}

// Runtime is implemented by the Bed composition layer, never by a carrier
// shell shortcut. Network returns a namespace scope and carrier-reachable IP.
type Runtime interface {
	Start(context.Context, Launch) (Process, error)
	Network() (scope, host string, err error)
}

// Status references port names; Network owns all allocated port facts.
type Status struct {
	protocol.PillowStatus
	// The internal outcome retains executor diagnostics. Public snapshots clear Detail.
	Outcome *executor.ProcessOutcome `json:"outcome,omitempty"`
}

// Access snapshots the current execution's ports under the Pillow lock.
// Callers own application protocols, credentials and reachable carrier addresses.
type Access struct {
	Ports []network.PortMappingStatus `json:"ports,omitempty"`
}
type Manager struct {
	bed.Noop
	configurations  *configuration.Manager
	mappings        *network.PortMappings
	mu              sync.Mutex
	closed          bool
	groups          map[*bed.Bed]*group
	onChange        func(*bed.Bed)
	inspectListener func(context.Context, int, string) (hostnetwork.ListenerInspection, error)
}
type group struct {
	mu      sync.Mutex
	bed     *bed.Bed
	runtime Runtime
	records []*record
	changed chan struct{}
	stopped bool
}
type record struct {
	mappings []*network.PortMapping
	spec     bed.PillowSpec
	status   Status
	restart  chan struct{}
	cancel   context.CancelFunc
	done     chan struct{}
}

func NewManager(mappings *network.PortMappings, onChange func(*bed.Bed)) *Manager {
	configurations, _ := configuration.NewManager(configuration.Config{})
	return &Manager{configurations: configurations, mappings: mappings, groups: make(map[*bed.Bed]*group), onChange: onChange, inspectListener: hostnetwork.InspectTCPListener}
}

// SetConfigurationManager binds the shared configuration domain before Start.
func (m *Manager) SetConfigurationManager(c *configuration.Manager) { m.configurations = c }
func (m *Manager) Resolve(specs []bed.PillowSpec) ([]bed.PillowSpec, error) {
	resolved, err := Normalize(specs)
	if err != nil {
		return nil, err
	}
	if len((bed.Spec{Pillows: resolved}).NetworkPorts()) > 0 && !m.mappings.Available() {
		return nil, fmt.Errorf("Pillow ports require a port manager")
	}
	return resolved, nil
}

func (m *Manager) PrepareBed(ctx context.Context, b *bed.Bed, runtime Runtime) error {
	specs, err := m.Resolve(b.Spec().Pillows)
	if err != nil {
		return err
	}
	g := &group{bed: b, runtime: runtime, changed: make(chan struct{})}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return fmt.Errorf("pillow manager closed")
	}
	if m.groups[b] != nil {
		m.mu.Unlock()
		return fmt.Errorf("pillows already prepared")
	}
	for _, spec := range specs {
		r := &record{spec: spec, status: Status{PillowStatus: protocol.PillowStatus{Name: spec.Name, Ports: portStatusNames(spec), Required: spec.Required, Phase: "starting"}}, restart: make(chan struct{}, 1), done: make(chan struct{})}
		g.records = append(g.records, r)
	}
	m.groups[b] = g
	// Install every cancel handle before exposing this group to Stop.
	for _, r := range g.records {
		if len(r.spec.Command) == 0 {
			r.status.Phase = "prepared"
			r.status.Ready = true
			r.status.Prepared = true
			r.cancel = func() {}
			close(r.done)
			continue
		}
		r.status.Prepared = true
		runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		r.cancel = cancel
		go m.supervise(runCtx, g, r)
	}
	m.mu.Unlock()
	for {
		g.mu.Lock()
		ready, failed := groupReadiness(g)
		changed := g.changed
		g.mu.Unlock()
		if failed != "" {
			return fmt.Errorf("required pillow %s failed", failed)
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
func groupReadiness(g *group) (bool, string) {
	ready := !g.stopped
	for _, r := range g.records {
		if r.spec.Required && !r.status.Ready {
			ready = false
			if r.status.Phase == "failed" || r.status.Phase == "stopped" || r.status.Phase == "completed" {
				return false, r.spec.Name
			}
		}
	}
	return ready, ""
}
func (m *Manager) group(b *bed.Bed) *group { m.mu.Lock(); defer m.mu.Unlock(); return m.groups[b] }
func (m *Manager) Ready(b *bed.Bed) bool {
	g := m.group(b)
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	ready, _ := groupReadiness(g)
	return ready
}
func (m *Manager) Status(b *bed.Bed) []Status {
	out := []Status{}
	g := m.group(b)
	if g == nil {
		return out
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, r := range g.records {
		s := r.status
		s.Ports = slices.Clone(s.Ports)
		if s.Listener != nil {
			v := *s.Listener
			s.Listener = &v
		}
		if s.Outcome != nil {
			v := *s.Outcome
			v.Detail = ""
			s.Outcome = &v
		}
		out = append(out, s)
	}
	return out
}
func (m *Manager) Access(b *bed.Bed, name string) (Access, error) {
	g := m.group(b)
	if g == nil {
		return Access{}, fmt.Errorf("pillow not found")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, r := range g.records {
		if r.spec.Name != name || g.stopped || !r.status.Ready || len(r.mappings) == 0 {
			continue
		}
		access := Access{}
		for _, mapping := range r.mappings {
			p := mapping.Status()
			if p.State != "listening" || p.ExecutionID != r.status.ExecutionID {
				return Access{}, fmt.Errorf("pillow ports unavailable")
			}
			access.Ports = append(access.Ports, p)
		}
		return access, nil
	}
	return Access{}, fmt.Errorf("pillow ports unavailable")
}

func portStatusNames(s bed.PillowSpec) []string {
	var names []string
	for _, p := range s.Ports {
		names = append(names, p.Name)
	}
	return names
}

func (m *Manager) Restart(b *bed.Bed, name string) error {
	g := m.group(b)
	if g == nil {
		return fmt.Errorf("pillow not found")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return fmt.Errorf("pillows stopping")
	}
	for _, r := range g.records {
		if r.spec.Name == name {
			if len(r.spec.Command) == 0 {
				return fmt.Errorf("pillow %s has no main process", name)
			}
			select {
			case r.restart <- struct{}{}:
			default:
			}
			return nil
		}
	}
	return fmt.Errorf("pillow not found")
}

// +rule=`Only a running Pillow can be ready. Leaving the execution lifecycle withdraws admission, but losing readiness never ends the execution.`
func (m *Manager) update(g *group, r *record, fn func(*Status)) {
	g.mu.Lock()
	before := r.status
	fn(&r.status)
	if r.status.Phase != "running" {
		r.status.Ready = false
	}
	after := r.status
	close(g.changed)
	g.changed = make(chan struct{})
	g.mu.Unlock()
	if before.Phase != after.Phase || before.Ready != after.Ready || before.ExecutionID != after.ExecutionID || before.Reason != after.Reason {
		log.Printf("hostel pillow transition: bed=%s id=%s pillow=%s phase=%s ready=%t execution=%s reason=%s", g.bed.Name, g.bed.ID, after.Name, after.Phase, after.Ready, after.ExecutionID, after.Reason)
	}
	if m.onChange != nil {
		m.onChange(g.bed)
	}
}
func (m *Manager) Stop(ctx context.Context, b *bed.Bed) error {
	g := m.group(b)
	return stopGroup(ctx, g)
}

func stopGroup(ctx context.Context, g *group) error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	g.stopped = true
	for _, r := range g.records {
		r.cancel()
	}
	g.mu.Unlock()
	for _, r := range g.records {
		select {
		case <-r.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (m *Manager) Release(ctx context.Context, b *bed.Bed) error {
	g := m.group(b)
	if err := stopGroup(ctx, g); err != nil {
		return err
	}
	m.mu.Lock()
	if m.groups[b] == g {
		delete(m.groups, b)
	}
	m.mu.Unlock()
	return nil
}
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	beds := make([]*bed.Bed, 0, len(m.groups))
	for b := range m.groups {
		beds = append(beds, b)
	}
	m.mu.Unlock()
	var result error
	for _, b := range beds {
		result = errors.Join(result, m.Release(ctx, b))
	}
	return result
}
