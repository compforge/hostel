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
	"github.com/qiankunli/hostel/internal/bed/service"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

var ErrServicesConflict = errors.New("bed: cannot change declared services")

func WithServices(ports *hostnetwork.PortManager, advertise string) ManagerOption {
	return func(m *Manager) {
		m.ports = ports
		m.services = service.NewManager(ports, advertise, m.servicesChanged)
	}
}

func (m *Manager) PortStatus() []hostnetwork.PortStatus {
	if m.ports == nil {
		return []hostnetwork.PortStatus{}
	}
	return m.ports.Status()
}
func (m *Manager) Services() *service.Manager { return m.services }

func (m *Manager) ServiceLogs(b *Resident, id string, cursor int64) ([]ExecutionOutput, int64, bool, bool, error) {
	e, ok := m.executions.Get(id)
	if !ok || e.BedID != b.Name || e.Mode != ExecutionService {
		return nil, 0, false, false, fmt.Errorf("service execution not found")
	}
	out, next, running, truncated := e.Logs(cursor)
	return out, next, running, truncated, nil
}

func (m *Manager) servicesChanged(b *model.Bed) {
	m.serviceStatusMu.Lock()
	defer m.serviceStatusMu.Unlock()
	ready := m.services.Ready(b)
	m.owners.Lifecycle.Update(b, func(s *model.LifecycleStatus) {
		if s.Phase != PhaseResident {
			return
		}
		s.Ready = ready
		s.UpdatedAt = time.Now()
		if ready {
			s.Reason = "Initialized"
		} else {
			s.Reason = "RequiredServiceUnavailable"
		}
	})
}

func checkServices(options CreateOptions, actual []model.ServiceSpec) error {
	if options.lookup {
		return nil
	}
	if len(actual) == 0 && len(options.Services) == 0 {
		return nil
	}
	if !reflect.DeepEqual(actual, options.Services) {
		return ErrServicesConflict
	}
	return nil
}

type serviceRuntime struct {
	manager *Manager
	bed     *managedBed
}

func (r serviceRuntime) Network() (string, string, error) {
	attachment := r.bed.environment.network
	if attachment == nil {
		return "", "127.0.0.1", nil
	}
	addressed, ok := attachment.(interface{ Address() string })
	if !ok || addressed.Address() == "" {
		return "", "", fmt.Errorf("Bed network does not provide a reachable service address")
	}
	return r.bed.ID.String(), addressed.Address(), nil
}
func (r serviceRuntime) Start(ctx context.Context, launch service.Launch) (service.Process, error) {
	b, m := r.bed, r.manager
	cwd, err := b.filesystem.Resolve(launch.Directory)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(launch.Command[0], launch.Command[1:]...)
	cmd.Env, err = m.buildBedEnv(b, launch.Env)
	if err != nil {
		return nil, err
	}
	if err := b.environment.Wrap(cmd, cwd); err != nil {
		return nil, err
	}
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
			StartService(context.Context, string, *exec.Cmd) (executor.Process, error)
		})
		if !ok {
			err = fmt.Errorf("executor does not support supervised service groups")
		} else {
			proc, err = starter.StartService(ctx, "process-"+randx.Hex(8), cmd)
		}
	}
	if err != nil {
		outR.Close()
		errR.Close()
		return nil, err
	}
	// Supervision owns cancellation and graceful stop. Do not turn a service
	// into a permanent Bed operation or couple it to the create HTTP request.
	execution := m.executions.track(context.WithoutCancel(ctx), b.Name, ExecutionService, e.ID(), e.Backend(), proc, outR, errR, 0, "", nil, nil, nil, nil)
	return &serviceProcess{process: proc, execution: execution}, nil
}

type serviceProcess struct {
	process   executor.Process
	execution *Execution
}

func (p *serviceProcess) PID() int                         { return p.process.PID() }
func (p *serviceProcess) ExecutionID() string              { return p.execution.ID }
func (p *serviceProcess) ExecutorID() string               { return p.execution.ExecutorID }
func (p *serviceProcess) Done() <-chan struct{}            { return p.execution.done }
func (p *serviceProcess) Outcome() executor.ProcessOutcome { return *p.execution.Wait().Process }
func (p *serviceProcess) Stop(ctx context.Context, grace time.Duration) error {
	select {
	case <-p.execution.done:
		return nil
	default:
	}
	p.execution.requestStop(CauseServiceStop, func() {
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

// HoldService protects direct requests and service-internal asynchronous work
// from normal/explicit eviction. It is a bounded operation, not a permanent
// activity claim made by the background service itself.
func (m *Manager) HoldService(b *Resident, duration time.Duration) (ServiceHold, error) {
	if duration <= 0 || duration > MaxOperationTimeout {
		return ServiceHold{}, fmt.Errorf("service hold must be positive and at most 2 hours")
	}
	finish, err := m.BeginOperation(b, OpService, duration)
	if err != nil {
		return ServiceHold{}, err
	}
	hold := &serviceHold{bed: b, view: ServiceHold{ID: "hold-" + randx.Hex(16), ExpiresAt: time.Now().Add(duration)}}
	var once sync.Once
	hold.finish = func() {
		once.Do(func() {
			m.mu.Lock()
			delete(m.serviceHolds, hold.view.ID)
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
	if m.serviceHolds == nil {
		m.serviceHolds = make(map[string]*serviceHold)
	}
	m.serviceHolds[hold.view.ID] = hold
	hold.timer = time.AfterFunc(duration, hold.finish)
	hold.stop = context.AfterFunc(m.cleanupCtx, hold.finish)
	m.mu.Unlock()
	return hold.view, nil
}

type ServiceHold struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
}
type serviceHold struct {
	bed    *Resident
	view   ServiceHold
	finish func()
	timer  *time.Timer
	stop   func() bool
}

func (m *Manager) ReleaseServiceHold(b *Resident, id string) {
	m.mu.Lock()
	hold := m.serviceHolds[id]
	m.mu.Unlock()
	if hold != nil && hold.bed == b {
		hold.finish()
	}
}
