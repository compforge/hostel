package executor

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/qiankunli/hostel/internal/bed/resource"
	"github.com/qiankunli/hostel/internal/bed/tool"
)

func pidnsStatus(policy tool.Policy, probed, selected bool, reason string) tool.Status {
	return tool.Describe(policy, tool.Requirements{Capabilities: []string{"CAP_SYS_ADMIN"},
		Conditions: []string{"Linux supervisor", "PID and mount namespace creation", "procfs mount permitted"}}, probed, selected, selected, reason)
}

// ResolveFactory only falls back during startup selection. A frozen combination
// must be recreated exactly, even when its original policy was auto.
func ResolveFactory(ctx context.Context, cfg Config, resources resource.Tracker) (Factory, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := cfg.PIDNS.Validate(); err != nil {
		return nil, err
	}
	if cfg.Backend != "" && cfg.Backend != "auto" && cfg.Backend != "supervisor" && cfg.Backend != "local" {
		return nil, fmt.Errorf("invalid executor backend %q", cfg.Backend)
	}
	policy := cfg.PIDNS.Effective()
	status := pidnsStatus(policy, false, false, "")
	private := policy != tool.Off
	if cfg.selection != nil {
		status, private = *cfg.selection, cfg.selection.Selected
	}
	if cfg.Backend == "local" {
		if policy == tool.Required || private && cfg.selection != nil {
			return nil, fmt.Errorf("PID namespace requires the supervisor executor")
		}
		if cfg.selection == nil && policy != tool.Off {
			status = pidnsStatus(policy, false, false, "local executor does not support PID namespaces")
		}
		f := NewLocalFactory(resources)
		f.pidns = status
		return f, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	factory, err := NewSupervisorFactory(exe, resources)
	if err != nil {
		return nil, err
	}
	factory.privatePIDNamespace = private
	err = factory.Probe(ctx)
	if private && cfg.selection == nil {
		reason := ""
		if err != nil {
			reason = err.Error()
		}
		status = pidnsStatus(policy, true, err == nil, reason)
	}
	var cleanupErr *ProbeCleanupError
	if errors.As(err, &cleanupErr) {
		return nil, err
	}
	if err != nil && private && policy == tool.Auto && cfg.selection == nil {
		// Probe fully shuts its candidate down before a weaker candidate starts.
		factory.privatePIDNamespace = false
		err = factory.Probe(ctx)
	}
	if errors.As(err, &cleanupErr) {
		return nil, err
	}
	if err == nil {
		factory.pidns = status
		return factory, nil
	}
	if cleanupErr := factory.Close(); cleanupErr != nil {
		return nil, fmt.Errorf("supervisor probe cleanup after %v: %w", err, cleanupErr)
	}
	if cfg.Backend == "supervisor" || policy == tool.Required || cfg.selection != nil {
		return nil, fmt.Errorf("required supervisor executor: %w", err)
	}
	log.Printf("hostel: executor backend=local reason=%q", err)
	f := NewLocalFactory(resources)
	f.pidns = status
	return f, nil
}
