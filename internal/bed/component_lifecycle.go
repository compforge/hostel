package bed

import (
	"context"
	"log"
	"time"

	"github.com/qiankunli/hostel/internal/amenity"
	"github.com/qiankunli/hostel/internal/executor"
	"github.com/qiankunli/hostel/internal/isolation"
	"github.com/qiankunli/hostel/internal/lifecycle"
	"github.com/qiankunli/hostel/internal/network"
	"github.com/qiankunli/hostel/internal/resource"
	"github.com/qiankunli/hostel/internal/store"
)

// These sequences are owned by this resident allocation and protected by
// Bed.cleanupMu. Their order is explicit rather than registration-dependent.
func (m *Manager) bindRuntimeLifecycle(b *Bed, storage *store.Binding, files *isolation.FilesBinding, net *network.Binding, resources *resource.Binding) {
	realm := executor.Bind(m.executorFactory, b.shutdownExecutor)
	b.stopSequence = lifecycle.NewSequence(lifecycle.Stop,
		lifecycle.Participant{Name: "store", Lifecycle: storage},
		lifecycle.Participant{Name: "executor", Lifecycle: realm},
	)
	b.releaseSequence = lifecycle.NewSequence(lifecycle.Release,
		lifecycle.Participant{Name: "amenity", Lifecycle: amenity.Bind(m.amenities, b.ID)},
		lifecycle.Participant{Name: "resource", Lifecycle: resources},
		lifecycle.Participant{Name: "network", Lifecycle: net},
		lifecycle.Participant{Name: "filesystem", Lifecycle: files},
	)
}

// teardown callers hold the allocation's cleanupMu. The bounded context is
// independent of an expired user request so cleanup can still make progress.
func (m *Manager) teardown(b *Bed) error {
	if b.runtimeClosed {
		return nil
	}
	m.executions.killBed(b.ID, CauseBedTeardown)
	m.revokeSessions(b)
	ctx, cancel := context.WithTimeout(context.Background(), 13*time.Second)
	defer cancel()
	if err := b.stopSequence.Run(ctx); err != nil {
		log.Printf("hostel bed teardown failed: bed=%s err=%v", b.ID, err)
		return err
	}
	if err := b.releaseSequence.Run(ctx); err != nil {
		log.Printf("hostel bed teardown failed: bed=%s err=%v", b.ID, err)
		return err
	}
	b.runtimeClosed = true
	return nil
}
