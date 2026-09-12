package manager

import (
	"context"
	model "github.com/qiankunli/hostel/internal/bed"
	"log"
	"time"
)

// Bed Manager owns the explicit cross-domain order and retry cursor; domains
// receive the exact shared Bed whose resources they allocated.
func (m *Manager) bindRuntimeLifecycle(b *managedBed) {
	b.stopSequence = model.NewSequence(model.Stop,
		model.Participant{Name: "store", Lifecycle: m.store},
		model.Participant{Name: "executor", Lifecycle: m.executorManager},
	)
	b.releaseSequence = model.NewSequence(model.Release,
		model.Participant{Name: "amenity", Lifecycle: m.amenities},
		model.Participant{Name: "executor", Lifecycle: m.executorManager},
		model.Participant{Name: "resource", Lifecycle: m.resourceManager},
		model.Participant{Name: "network", Lifecycle: m.network},
		model.Participant{Name: "filesystem", Lifecycle: m.files},
		model.Participant{Name: "store", Lifecycle: m.store},
	)
}

// The cleanup owner serializes retries independently of user request expiry.
func (m *Manager) teardown(b *managedBed) error {
	if b.runtimeClosed {
		return nil
	}
	m.owners.Lifecycle.Set(b.Bed, model.LifecycleStatus{Phase: "stopping", Reason: "RuntimeCleanup"})
	m.executions.killBed(b.ID, CauseBedTeardown)
	m.revokeSessions(b)
	ctx, cancel := context.WithTimeout(context.Background(), 13*time.Second)
	defer cancel()
	if err := b.stopSequence.Run(ctx, b.Bed); err != nil {
		log.Printf("hostel bed stop failed: bed=%s instance=%d err=%v", b.ID, b.InstanceID, err)
		return err
	}
	if err := b.releaseSequence.Run(ctx, b.Bed); err != nil {
		log.Printf("hostel bed release failed: bed=%s instance=%d err=%v", b.ID, b.InstanceID, err)
		return err
	}
	b.runtimeClosed = true
	m.owners.Lifecycle.Set(b.Bed, model.LifecycleStatus{Phase: "dormant", Reason: "RuntimeReleased"})
	return nil
}
