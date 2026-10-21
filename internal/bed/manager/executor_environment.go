package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sort"
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
	views       []io.Closer
	pillows     map[string]*Environment
	mu          sync.RWMutex
	closed      bool
	closeErr    error
}

func (e *Environment) bindExecutor(ctx context.Context, raw executor.Executor, privatePID bool) (executor.Executor, error) {
	bound := &environmentExecutor{Executor: raw, pillows: map[string]*Environment{}}
	bind := func(template *Environment) (*Environment, error) {
		env, view, err := template.bindView(ctx, raw, privatePID)
		if view != nil {
			bound.views = append(bound.views, view)
		}
		return env, err
	}
	var err error
	bound.environment, err = bind(e)
	if err != nil {
		return nil, errors.Join(err, bound.closeView())
	}
	names := make([]string, 0, len(e.pillows))
	for name := range e.pillows {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if e.pillows[name] == e {
			bound.pillows[name] = bound.environment
			continue
		}
		bound.pillows[name], err = bind(e.pillows[name])
		if err != nil {
			return nil, errors.Join(fmt.Errorf("prepare pillow %s view: %w", name, err), bound.closeView())
		}
	}
	go func() {
		<-raw.Done()
		if err := bound.closeView(); err != nil {
			log.Printf("hostel: release Executor views: executor=%s error=%q", raw.ID(), err)
		}
	}()
	return bound, nil
}

func (e *Environment) bindView(ctx context.Context, raw executor.Executor, privatePID bool) (*Environment, io.Closer, error) {
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
			return nil, nil, err
		}
	}
	return bindEnvironment(files, e.fs, e.network, e.user), view, nil
}

func (e *environmentExecutor) closeView() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	for _, view := range e.views {
		e.closeErr = errors.Join(e.closeErr, view.Close())
	}
	e.views = nil
	return e.closeErr
}
func (e *environmentExecutor) Start(ctx context.Context, id string, cmd *exec.Cmd) (executor.Process, error) {
	environment := e.environment
	if name, ok := ctx.Value(pillowContextKey{}).(string); ok && name != "" {
		var found bool
		environment, found = e.pillows[name]
		if !found {
			return nil, fmt.Errorf("unknown pillow %q", name)
		}
	}
	return e.start(ctx, id, cmd, false, environment)
}
func (e *environmentExecutor) StartMain(ctx context.Context, id string, cmd *exec.Cmd) (executor.Process, error) {
	return e.start(ctx, id, cmd, true, e.environment)
}

// StartMainFor selects a trusted declaration, never a workload-controlled env value.
func (e *environmentExecutor) StartMainFor(ctx context.Context, name, id string, cmd *exec.Cmd) (executor.Process, error) {
	environment := e.environment
	if len(e.pillows) != 0 {
		var ok bool
		environment, ok = e.pillows[name]
		if !ok {
			return nil, fmt.Errorf("unknown pillow file view %q", name)
		}
	}
	return e.start(ctx, id, cmd, true, environment)
}
func (e *environmentExecutor) start(ctx context.Context, id string, cmd *exec.Cmd, pillow bool, environment *Environment) (executor.Process, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return nil, fmt.Errorf("Executor %s view is closed", e.ID())
	}
	if err := environment.Wrap(cmd, cmd.Dir); err != nil {
		return nil, err
	}
	if pillow {
		starter, ok := e.Executor.(interface {
			StartMain(context.Context, string, *exec.Cmd) (executor.Process, error)
		})
		if !ok {
			return nil, fmt.Errorf("executor does not support supervised pillow groups")
		}
		return starter.StartMain(ctx, id, cmd)
	}
	return e.Executor.Start(ctx, id, cmd)
}
func (e *environmentExecutor) Shutdown(ctx context.Context) error {
	if err := e.Executor.Shutdown(ctx); err != nil {
		return err
	}
	return e.closeView()
}
