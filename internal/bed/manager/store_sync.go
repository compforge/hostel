package manager

import (
	"context"
	"time"
)

func (m *Manager) RequestStoreSync() { m.store.RequestSync() }

// RunStoreSync connects the daemon Store controller to the Manager's safe Bed
// enumeration and persistence protocol. It does not start another sync loop.
func (m *Manager) RunStoreSync(ctx context.Context, interval time.Duration) {
	m.store.ConfigureSync(interval, m.persistDirty)
	_ = m.store.Run(ctx)
}

func (m *Manager) SetPersistInterval(interval time.Duration) {
	m.store.ConfigureSync(interval, m.persistDirty)
}
