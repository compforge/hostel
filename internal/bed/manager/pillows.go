package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"sync"
	"syscall"
	"time"

	"github.com/qiankunli/go-stdx/randx"
	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/bed/pillow"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

var ErrPillowsConflict = errors.New("bed: cannot change declared pillows")

func WithPillows(ports *hostnetwork.PortManager) ManagerOption {
	return func(m *Manager) {
		m.ports = ports
		m.portMappings = network.NewPortMappings(ports)
		m.pillows = pillow.NewManager(m.portMappings, m.pillowsChanged)
		m.network.SetPortMappings(m.portMappings)
	}
}

func (m *Manager) PortStatus() []hostnetwork.PortStatus {
	if m.ports == nil {
		return []hostnetwork.PortStatus{}
	}
	return m.ports.Status()
}
func (m *Manager) Pillows() *pillow.Manager { return m.pillows }
func (m *Manager) PortMappings(b *model.Bed) []network.PortMappingStatus {
	return m.portMappings.Status(b)
}

func (m *Manager) PillowLogs(b *Resident, id string, cursor int64) ([]ExecutionOutput, int64, bool, bool, error) {
	e, ok := m.executions.Get(id)
	if !ok || e.BedID != b.Name || e.Mode != ExecutionMain {
		return nil, 0, false, false, fmt.Errorf("pillow execution not found")
	}
	out, next, running, truncated := e.Logs(cursor)
	return out, next, running, truncated, nil
}

func (m *Manager) pillowsChanged(b *model.Bed) {
	m.pillowStatusMu.Lock()
	defer m.pillowStatusMu.Unlock()
	ready := m.pillows.Ready(b)
	m.owners.Lifecycle.Update(b, func(s *model.LifecycleStatus) {
		if s.Phase != PhaseResident {
			return
		}
		s.Ready = ready
		s.UpdatedAt = time.Now()
		if ready {
			s.Reason = "Initialized"
		} else {
			s.Reason = "RequiredPillowUnavailable"
		}
	})
}

func checkPillows(options CreateOptions, actual []model.PillowSpec) error {
	normalized, err := pillow.Normalize(actual)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(normalized, options.Pillows) {
		return ErrPillowsConflict
	}
	return nil
}

type pillowRuntime struct {
	manager *Manager
	bed     *managedBed
}

func (r pillowRuntime) Network() (string, string, error) {
	attachment := r.bed.environment.network
	if attachment == nil {
		return "", "127.0.0.1", nil
	}
	addressed, ok := attachment.(interface{ Address() string })
	if !ok || addressed.Address() == "" {
		return "", "", fmt.Errorf("Bed network does not provide a reachable pillow address")
	}
	return r.bed.ID.String(), addressed.Address(), nil
}
func (r pillowRuntime) Start(ctx context.Context, launch pillow.Launch) (pillow.Process, error) {
	b, m := r.bed, r.manager
	environment := b.environment
	if view, ok := environment.pillows[launch.Name]; ok {
		environment = view
	}
	cwd, err := environment.fs.Resolve(launch.Directory)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(launch.Command[0], launch.Command[1:]...)
	cmd.Env, err = m.buildBedEnvWithView(b, environment.View(), launch.Env)
	if err != nil {
		return nil, err
	}
	cmd.Dir = cwd
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return nil, err
	}
	defer outW.Close()
	defer errW.Close()
	cmd.Stdout, cmd.Stderr = outW, errW
	e, err := m.executorManager.For(ctx, b.Bed)
	var proc executor.Process
	if err == nil {
		starter, ok := e.(interface {
			StartMainFor(context.Context, string, string, *exec.Cmd) (executor.Process, error)
		})
		if !ok {
			err = fmt.Errorf("executor does not support supervised pillow groups")
		} else {
			proc, err = starter.StartMainFor(ctx, launch.Name, "process-"+randx.Hex(8), cmd)
		}
	}
	if err != nil {
		outR.Close()
		errR.Close()
		return nil, err
	}
	// Supervision owns cancellation and graceful stop. Do not turn a pillow
	// into a permanent Bed operation or couple it to the create HTTP request.
	execution := m.executions.track(withPillow(context.WithoutCancel(ctx), launch.Name), b.Name, ExecutionMain, e.ID(), e.Backend(), proc, outR, errR, 0, "", nil, nil, nil, nil)
	return &pillowProcess{process: proc, execution: execution}, nil
}

type pillowProcess struct {
	process   executor.Process
	execution *Execution
}

func (p *pillowProcess) PID() int                         { return p.process.PID() }
func (p *pillowProcess) ExecutionID() string              { return p.execution.ID }
func (p *pillowProcess) ExecutorID() string               { return p.execution.ExecutorID }
func (p *pillowProcess) Done() <-chan struct{}            { return p.execution.done }
func (p *pillowProcess) Outcome() executor.ProcessOutcome { return *p.execution.Wait().Process }
func (p *pillowProcess) Stop(ctx context.Context, grace time.Duration) error {
	select {
	case <-p.execution.done:
		return nil
	default:
	}
	p.execution.requestStop(CauseMainStop, func() {
		if signaler, ok := p.process.(interface{ Signal(syscall.Signal) error }); ok {
			if err := signaler.Signal(syscall.SIGTERM); err != nil {
				p.process.Kill()
			}
		} else {
			p.process.Kill()
		}
	})
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-p.execution.done:
		return nil
	case <-ctx.Done():
		p.process.Kill()
		return ctx.Err()
	case <-timer.C:
		p.process.Kill()
	}
	select {
	case <-p.execution.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// HoldPillow protects direct requests and pillow-internal asynchronous work
// from normal/explicit eviction. It is a bounded operation, not a permanent
// activity claim made by the background pillow itself.
func (m *Manager) HoldPillow(b *Resident, duration time.Duration) (PillowHold, error) {
	if duration <= 0 || duration > MaxOperationTimeout {
		return PillowHold{}, fmt.Errorf("pillow hold must be positive and at most 2 hours")
	}
	finish, err := m.BeginOperation(b, OpPillow, duration)
	if err != nil {
		return PillowHold{}, err
	}
	hold := &pillowHold{bed: b, view: PillowHold{ID: "hold-" + randx.Hex(16), ExpiresAt: time.Now().Add(duration)}}
	var once sync.Once
	hold.finish = func() {
		once.Do(func() {
			m.mu.Lock()
			delete(m.pillowHolds, hold.view.ID)
			timer, stop := hold.timer, hold.stop
			m.mu.Unlock()
			if timer != nil {
				timer.Stop()
			}
			if stop != nil {
				stop()
			}
			finish()
		})
	}
	m.mu.Lock()
	if m.pillowHolds == nil {
		m.pillowHolds = make(map[string]*pillowHold)
	}
	m.pillowHolds[hold.view.ID] = hold
	hold.timer = time.AfterFunc(duration, hold.finish)
	hold.stop = context.AfterFunc(m.cleanupCtx, hold.finish)
	m.mu.Unlock()
	return hold.view, nil
}

type PillowHold struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
}
type pillowHold struct {
	bed    *Resident
	view   PillowHold
	finish func()
	timer  *time.Timer
	stop   func() bool
}

func (m *Manager) ReleasePillowHold(b *Resident, id string) {
	m.mu.Lock()
	hold := m.pillowHolds[id]
	m.mu.Unlock()
	if hold != nil && hold.bed == b {
		hold.finish()
	}
}
