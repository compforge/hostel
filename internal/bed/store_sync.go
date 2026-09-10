package bed

import (
	"context"
	"time"
)

func (m *Manager) RequestStoreSync() { m.store.RequestSync() }

// RunStoreSync connects the daemon Store controller to the Manager's safe Bed
// enumeration and persistence protocol. It does not start another sync loop.
func (m *Manager) RunStoreSync(ctx context.Context, interval time.Duration) {
	m.store.Run(ctx, interval, m.persistDirty)
}
