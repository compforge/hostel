package bed

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/qiankunli/go-stdx/randx"
	"github.com/qiankunli/hostel/internal/executor"
	"github.com/qiankunli/hostel/internal/store"
)

// ProbeEnvironment exercises the selected composition through the same Bed,
// command and session entry points used by clients. Run before HTTP admission.
// A failed combination is a startup error, not permission to run unconfined.
func (m *Manager) ProbeEnvironment(ctx context.Context) (retErr error) {
	id := "environment-probe-" + randx.Hex(6)
	initialization, _, err := m.beginInitialization(ctx, id, CreateOptions{Store: string(store.KindNoop)})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		retErr = errors.Join(retErr, m.Purge(cleanup, id))
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-initialization.done:
	}
	if initialization.err != nil {
		return initialization.err
	}
	b := initialization.bed
	cwd, err := b.BedFS().Resolve("/workspace/.hostel-environment-probe")
	if err != nil {
		return err
	}
	if err := b.BedFS().EnsureDir(cwd); err != nil {
		return err
	}
	const command = "test -w . && printf probe > marker && test -s marker"
	result, err := m.RunForeground(ctx, b, command, cwd, nil, 5*time.Second, nil)
	if err != nil {
		return err
	}
	if result.Process.Kind != executor.ProcessExited || result.Process.ExitCode != 0 {
		return fmt.Errorf("isolation: combined command probe failed: %+v", result.Process)
	}
	if _, err := b.BedFS().Stat("/workspace/.hostel-environment-probe/marker"); err != nil {
		return fmt.Errorf("isolation: command/file view probe: %w", err)
	}
	shellID, err := m.CreateShell(b, cwd)
	if err != nil {
		return err
	}
	shell, _ := b.GetShell(shellID)
	defer shell.Close()
	run, err := shell.Run(ctx, command, nil)
	if err != nil {
		return err
	}
	if run.ExitCode != 0 {
		return fmt.Errorf("isolation: combined shell probe exited %d", run.ExitCode)
	}
	return nil
}
