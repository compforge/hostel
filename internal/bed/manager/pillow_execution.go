package manager

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
)

type pillowContextKey struct{}

func withPillow(ctx context.Context, name string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, pillowContextKey{}, name)
}
func selectedPillow(ctx context.Context, b *Resident) string {
	if ctx != nil {
		if name, ok := ctx.Value(pillowContextKey{}).(string); ok && name != "" {
			return name
		}
	}
	return b.Spec().DefaultPillow().Name
}
func (b *managedBed) pillowEnvironment(name string) (*Environment, error) {
	if name == "" {
		name = b.Spec().DefaultPillow().Name
	}
	e, ok := b.environment.pillows[name]
	if !ok {
		return nil, fmt.Errorf("unknown pillow %q", name)
	}
	return e, nil
}

// PillowFiles is a trusted management operation. Data-plane adapters use Files() only.
func (m *Manager) PillowFiles(b *Resident, name string) (*bedfs.FS, error) {
	e, err := b.pillowEnvironment(name)
	if err != nil {
		return nil, err
	}
	return e.fs, nil
}

// StartPillowExecution runs in a declared environment without changing the default Pillow.
func (m *Manager) StartPillowExecution(ctx context.Context, b *Resident, name string, mode ExecutionMode, command, cwd, stdin string, env map[string]string, timeout time.Duration, onStart func(ExecutionStatus), onOutput func(ExecutionOutput)) (*Execution, error) {
	if _, err := b.pillowEnvironment(name); err != nil {
		return nil, err
	}
	return m.StartExecution(withPillow(ctx, name), b, mode, command, cwd, stdin, env, timeout, onStart, onOutput)
}

type pillowExecutor struct {
	executor.Executor
	name string
}

func (e pillowExecutor) Start(ctx context.Context, id string, cmd *exec.Cmd) (executor.Process, error) {
	return e.Executor.Start(withPillow(ctx, e.name), id, cmd)
}
