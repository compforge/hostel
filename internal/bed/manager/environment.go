package manager

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"time"

	"github.com/qiankunli/go-stdx/randx"
	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/service"
	"github.com/qiankunli/hostel/internal/bed/store"
)

// ProbeCleanupError prevents startup fallback from abandoning an allocation.
type ProbeCleanupError struct{ Err error }

func (e *ProbeCleanupError) Error() string { return "environment probe cleanup: " + e.Err.Error() }
func (e *ProbeCleanupError) Unwrap() error { return e.Err }

// ProbeEnvironment exercises the selected composition through the same Bed,
// command and session entry points used by clients. Run before HTTP admission.
// A failed combination is a startup error, not permission to run unconfined.
func (m *Manager) ProbeEnvironment(ctx context.Context) (retErr error) {
	started := time.Now()
	m.startEnvironmentProbe(started)
	defer func() { m.finishEnvironmentProbe(started, retErr) }()
	id := "environment-probe-" + randx.Hex(6)
	initialization, _, err := m.beginInitialization(ctx, id, CreateOptions{Sync: string(store.SyncNoop)})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.Purge(cleanup, id); err != nil {
			retErr = errors.Join(retErr, &ProbeCleanupError{Err: err})
		}
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
	// Use absolute core-tool paths: workspace helper tests may deliberately
	// narrow PATH, but that must not disable the process-credential probe.
	command := "test -w . && printf probe > marker && test -s marker" +
		" && test \"$(/usr/bin/id -u)\" = " + strconv.Itoa(b.environment.BedUser().UID()) +
		" && test \"$(/usr/bin/id -g)\" = " + strconv.Itoa(b.environment.BedUser().GID())
	if runtime.GOOS == "linux" {
		capabilitySets := "Inh|Prm|Eff|Amb"
		if b.environment.BedUser().UID() == 0 {
			capabilitySets += "|Bnd"
		}
		command += " && /usr/bin/awk '$1 == \"NoNewPrivs:\" { n=$2 } $1 ~ /^Cap(" + capabilitySets + "):/ && $2 != \"0000000000000000\" { bad=1 } END { exit (bad || n != 1) }' /proc/self/status"
	}
	// This is part of the selected composition probe: advertised process paths
	// must reach BedFS through every process entry point, not just through uploads.
	command += ` && printf temporary >> "$TMPDIR/.hostel-environment-probe"`
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
	// Exercise the Service process entry point too, without defining a hosted
	// application or making a readiness/restart policy part of the probe.
	p, err := (serviceRuntime{manager: m, bed: b}).Start(ctx, service.Launch{
		Command:   []string{"/bin/sh", "-c", command + " && printf service > service-marker"},
		Directory: "/workspace/.hostel-environment-probe",
	})
	if err != nil {
		return fmt.Errorf("isolation: combined service probe: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.Stop(cleanup, time.Second); err != nil {
			retErr = errors.Join(retErr, &ProbeCleanupError{Err: err})
		}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.Done():
	}
	if outcome := p.Outcome(); outcome.Kind != executor.ProcessExited || outcome.ExitCode != 0 {
		return fmt.Errorf("isolation: combined service probe failed: %+v", outcome)
	}
	if _, err := b.BedFS().Stat("/workspace/.hostel-environment-probe/service-marker"); err != nil {
		return fmt.Errorf("isolation: service/file view probe: %w", err)
	}
	data, err := b.BedFS().Read("/tmp/.hostel-environment-probe")
	if err != nil {
		return fmt.Errorf("isolation: temporary directory/file view probe: %w", err)
	}
	if string(data) != "temporarytemporarytemporary" {
		return fmt.Errorf("isolation: command, session and Service do not share the Bed temporary directory")
	}
	return nil
}
