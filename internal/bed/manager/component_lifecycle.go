package manager

import (
	"context"
	"log"
	"time"

	"github.com/qiankunli/hostel/internal/amenity"
	model "github.com/qiankunli/hostel/internal/bed"
)

// Bed Manager owns the explicit cross-domain order and retry cursor; domains
// receive the exact shared Bed whose resources they allocated.
func (m *Manager) bindRuntimeLifecycle(b *managedBed) {
	b.stopSequence = model.NewSequence(model.Stop,
		model.Participant{Name: "store", Lifecycle: m.store},
		model.Participant{Name: "executor", Lifecycle: m.executorManager},
	)
	b.releaseSequence = model.NewSequence(model.Release,
		model.Participant{Name: "amenity", Lifecycle: amenityLifecycle{manager: m.amenities}},
		model.Participant{Name: "executor", Lifecycle: m.executorManager},
		model.Participant{Name: "resource", Lifecycle: m.resourceManager},
		model.Participant{Name: "network", Lifecycle: m.network},
		model.Participant{Name: "filesystem", Lifecycle: m.files},
		model.Participant{Name: "store", Lifecycle: m.store},
	)
}

// The cleanup owner serializes retries independently of user request expiry.
func (m *Manager) teardown(ctx context.Context, b *managedBed) (retErr error) {
	if b.runtimeClosed {
		return nil
	}
	initializing := b.Bed.Status().Lifecycle.Phase == PhaseInitializing
	defer func() {
		if retErr != nil && !initializing {
			m.owners.Lifecycle.Set(b.Bed, model.LifecycleStatus{Phase: PhaseEvicting, Reason: "CleanupPending", Message: retErr.Error(), UpdatedAt: time.Now()})
		}
	}()
	// Initialization remains joinable while its rollback releases partial
	// resources. Preserve its failed stage until the owner publishes failure.
	if !initializing {
		m.owners.Lifecycle.Set(b.Bed, model.LifecycleStatus{Phase: PhaseEvicting, Reason: "RuntimeCleanup", UpdatedAt: time.Now()})
	}
	m.executions.killBed(b.Name, CauseBedTeardown)
	m.revokeSessions(ctx, b)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.stopSequence.Run(ctx, b.Bed); err != nil {
		log.Printf("hostel bed stop failed: bed=%s id=%s err=%v", b.Name, b.ID, err)
		return err
	}
	if err := b.releaseSequence.Run(ctx, b.Bed); err != nil {
		log.Printf("hostel bed release failed: bed=%s id=%s err=%v", b.Name, b.ID, err)
		return err
	}
	b.runtimeClosed = true
	if !initializing {
		m.mu.Lock()
		retiring := m.retirements[b.Name] == b
		m.mu.Unlock()
		phase, reason := PhaseDormant, "RuntimeReleased"
		if retiring {
			phase, reason = PhaseEvicting, "CleanupPending"
		}
		m.owners.Lifecycle.Set(b.Bed, model.LifecycleStatus{Phase: phase, Reason: reason, UpdatedAt: time.Now()})
	}
	return nil
}

// Rollback is detached from the initiating request but belongs to the daemon.
// Close can cancel and join it when its overall shutdown deadline expires.
func (m *Manager) rollback(b *managedBed) error {
	ctx, cancel := context.WithTimeout(m.cleanupCtx, 13*time.Second)
	defer cancel()
	return m.teardown(ctx, b)
}

func (m *Manager) cleanupContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	stop := context.AfterFunc(m.cleanupCtx, cancel)
	if m.cleanupCtx.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}

// amenityLifecycle only bridges Bed teardown; daemon owns facility lifecycles.
type amenityLifecycle struct {
	model.Noop
	manager *amenity.Manager
}

func (a amenityLifecycle) Release(ctx context.Context, b *model.Bed) error {
	return a.manager.ReleaseBed(ctx, b.ID)
}
