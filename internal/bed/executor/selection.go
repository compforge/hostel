package executor

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/qiankunli/hostel/internal/bed/resource"
)

// ResolveFactory probes the same backend for startup combinations and real Beds.
func ResolveFactory(ctx context.Context, cfg Config, resources resource.Tracker) (Factory, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if cfg.Backend == "local" {
		return NewLocalFactory(resources), nil
	}
	if cfg.Backend != "" && cfg.Backend != "auto" && cfg.Backend != "supervisor" {
		return nil, fmt.Errorf("invalid executor backend %q", cfg.Backend)
	}
	exe, err := os.Executable()
	var factory *SupervisorFactory
	if err == nil {
		factory, err = NewSupervisorFactory(exe, resources)
	}
	if err == nil {
		err = factory.Probe(ctx)
	}
	if err == nil {
		return factory, nil
	}
	if factory != nil {
		if cleanupErr := factory.Close(); cleanupErr != nil {
			return nil, fmt.Errorf("supervisor probe cleanup after %v: %w", err, cleanupErr)
		}
	}
	if cfg.Backend == "supervisor" {
		return nil, fmt.Errorf("required supervisor executor: %w", err)
	}
	log.Printf("hostel: executor backend=local reason=%q", err)
	return NewLocalFactory(resources), nil
}
