package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/executor"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

type Launch struct {
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
type Status struct {
	Name        string                          `json:"name"`
	Required    bool                            `json:"required"`
	Phase       string                          `json:"phase"`
	ExecutionID string                          `json:"execution_id,omitempty"`
	ExecutorID  string                          `json:"executor_id,omitempty"`
	Restarts    int                             `json:"restarts"`
	Endpoint    string                          `json:"endpoint,omitempty"`
	Reason      string                          `json:"reason,omitempty"`
	Outcome     *executor.ProcessOutcome        `json:"outcome,omitempty"`
	Listener    *hostnetwork.ListenerInspection `json:"listener,omitempty"`
}

// Access is a credential-bearing response, separate from diagnostic status.
type Access struct {
	Endpoint    string `json:"endpoint"`
	ExecutionID string `json:"execution_id"`
	Token       string `json:"token,omitempty"`
}
type Manager struct {
	bed.Noop
	ports           *hostnetwork.PortManager
	advertise       string
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
	spec    bed.ServiceSpec
	status  Status
	token   string
	restart chan struct{}
	cancel  context.CancelFunc
	done    chan struct{}
}

func NewManager(ports *hostnetwork.PortManager, advertise string, onChange func(*bed.Bed)) *Manager {
	return &Manager{ports: ports, advertise: advertise, groups: make(map[*bed.Bed]*group), onChange: onChange, inspectListener: hostnetwork.InspectTCPListener}
}
func (m *Manager) Resolve(specs []bed.ServiceSpec) ([]bed.ServiceSpec, error) {
	resolved, err := Normalize(specs)
	if err != nil {
		return nil, err
	}
	for _, s := range resolved {
		if s.HTTP != nil && (m.ports == nil || m.advertise == "") {
			return nil, fmt.Errorf("HTTP services require a port manager and advertised host")
		}
	}
	return resolved, nil
}

func (m *Manager) PrepareBed(ctx context.Context, b *bed.Bed, runtime Runtime) error {
	specs, err := m.Resolve(b.Spec().Services)
	if err != nil {
		return err
	}
	g := &group{bed: b, runtime: runtime, changed: make(chan struct{})}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return fmt.Errorf("service manager closed")
	}
	if m.groups[b] != nil {
		m.mu.Unlock()
		return fmt.Errorf("services already prepared")
	}
	for _, spec := range specs {
		r := &record{spec: spec, status: Status{Name: spec.Name, Required: spec.Required, Phase: "starting"}, restart: make(chan struct{}, 1), done: make(chan struct{})}
		g.records = append(g.records, r)
	}
	m.groups[b] = g
	// Install every cancel handle before exposing this group to Stop.
	for _, r := range g.records {
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
			return fmt.Errorf("required service %s failed", failed)
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
		if r.spec.Required && r.status.Phase != "ready" {
			ready = false
			if r.status.Phase == "failed" || r.status.Phase == "stopped" {
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
		return len(b.Spec().Services) == 0
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
		return Access{}, fmt.Errorf("service not found")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, r := range g.records {
		if r.spec.Name == name && !g.stopped && r.status.Phase == "ready" && r.status.Endpoint != "" {
			return Access{Endpoint: r.status.Endpoint, ExecutionID: r.status.ExecutionID, Token: r.token}, nil
		}
	}
	return Access{}, fmt.Errorf("HTTP service unavailable")
}
func (m *Manager) Restart(b *bed.Bed, name string) error {
	g := m.group(b)
	if g == nil {
		return fmt.Errorf("service not found")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return fmt.Errorf("services stopping")
	}
	for _, r := range g.records {
		if r.spec.Name == name {
			select {
			case r.restart <- struct{}{}:
			default:
			}
			return nil
		}
	}
	return fmt.Errorf("service not found")
}
func (m *Manager) update(g *group, r *record, fn func(*Status)) {
	g.mu.Lock()
	before := r.status
	fn(&r.status)
	after := r.status
	close(g.changed)
	g.changed = make(chan struct{})
	g.mu.Unlock()
	if before.Phase != after.Phase || before.ExecutionID != after.ExecutionID {
		log.Printf("hostel service transition: bed=%s id=%s service=%s phase=%s execution=%s reason=%s", g.bed.Name, g.bed.ID, after.Name, after.Phase, after.ExecutionID, after.Reason)
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
