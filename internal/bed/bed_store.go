package bed

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/qiankunli/hostel/internal/store"
)

// CreateOptions selects the backend when creating a Bed. An explicit Store
// overrides the instance default. After eviction, callers must repeat overrides
// because the local Bed metadata is removed together with its workspace.
type CreateOptions struct{ Store string }

var (
	ErrStoreInvalid  = errors.New("bed: unsupported store backend")
	ErrStoreConflict = errors.New("bed: cannot change an active bed's store")
)

// bedStore uses local metadata for an orphaned Bed. No separate routing record
// outlives its working copy; absent both an override and metadata, use default.
func (m *Manager) bedStore(ctx context.Context, id, requested string) (store.Store, error) {
	if requested == "" {
		meta, _ := loadMeta(filepath.Join(m.root, id))
		requested = meta.Store
	}
	selected, err := store.Select(ctx, m.store, requested)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStoreInvalid, err)
	}
	return selected, nil
}

// A create retry may reuse a live Bed, but cannot migrate its active backend.
func checkBedStore(requested string, selected, current store.Store) error {
	if requested != "" && selected.Name() != current.Name() {
		return ErrStoreConflict
	}
	return nil
}

func (b *Bed) StoreName() string { return b.store.Name() }
