package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
	"time"

	"github.com/qiankunli/go-stdx/randx"
	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
)

// environmentExecutor owns the realized view for exactly one process realm.
// The Bed keeps its template; replacing a realm never reuses its procfs mounts.
// +rule=`Compose before publishing the Executor; release after its processes exit, and serialize release with all Start entry points.`
type environmentExecutor struct {
	executor.Executor
	environment *Environment
	view        io.Closer
	mu          sync.RWMutex
	closed      bool
	closeErr    error
}

func (e *Environment) bindExecutor(ctx context.Context, raw executor.Executor, privatePID bool) (executor.Executor, error) {
	files := e.files
	var view io.Closer
	if privatePID {
		prepareCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var helper executor.Process
		var err error
		files, view, err = isolation.BindExecutorView(prepareCtx, files, e.fs, func(cmd *exec.Cmd) error {
			var startErr error
			helper, startErr = raw.Start(prepareCtx, "prepare-view-"+randx.Hex(8), cmd)
			if startErr != nil {
				return startErr
			}
			return nil
		})
		if helper != nil {
			if err != nil {
				helper.Kill()
			}
			result, waitErr := helper.Wait(prepareCtx)
			if waitErr != nil || result.Kind != executor.ProcessExited || result.ExitCode != 0 {
				helper.Kill()
				err = errors.Join(err, fmt.Errorf("prepare Executor view: outcome=%+v error=%v", result, waitErr))
			}
		}
		if err != nil {
			if view != nil {
				err = errors.Join(err, view.Close())
			}
			return nil, err
		}
	}
	bound := &environmentExecutor{Executor: raw, environment: bindEnvironment(files, e.fs, e.network, e.user), view: view}
	go func() {
		<-raw.Done()
		if err := bound.closeView(); err != nil {
			log.Printf("hostel: release Executor view: executor=%s error=%q", raw.ID(), err)
		}
	}()
	return bound, nil
}

func (e *environmentExecutor) closeView() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	if e.view != nil {
		e.closeErr = e.view.Close()
		e.view = nil
	}
	return e.closeErr
}
func (e *environmentExecutor) Start(ctx context.Context, id string, cmd *exec.Cmd) (executor.Process, error) {
	return e.start(ctx, id, cmd, false)
}
func (e *environmentExecutor) StartService(ctx context.Context, id string, cmd *exec.Cmd) (executor.Process, error) {
	return e.start(ctx, id, cmd, true)
}
func (e *environmentExecutor) start(ctx context.Context, id string, cmd *exec.Cmd, service bool) (executor.Process, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return nil, fmt.Errorf("Executor %s view is closed", e.ID())
	}
	if err := e.environment.Wrap(cmd, cmd.Dir); err != nil {
		return nil, err
	}
	if service {
		starter, ok := e.Executor.(interface {
			StartService(context.Context, string, *exec.Cmd) (executor.Process, error)
		})
		if !ok {
			return nil, fmt.Errorf("executor does not support supervised service groups")
		}
		return starter.StartService(ctx, id, cmd)
	}
	return e.Executor.Start(ctx, id, cmd)
}
func (e *environmentExecutor) Shutdown(ctx context.Context) error {
	if err := e.Executor.Shutdown(ctx); err != nil {
		return err
	}
	return e.closeView()
}
