package bed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/qiankunli/go-stdx/osx"
	"github.com/qiankunli/hostel/internal/store"
)

// CreateOptions belongs to the Bed identity, not its current Executor.
// Empty Store reuses a recorded choice, or inherits the instance default for
// a new identity. Explicit backend names override the instance default.
type CreateOptions struct{ Store string }

const (
	StoreDefault = "default"
	StoreNoop    = "noop"
)

var (
	ErrStoreInvalid  = errors.New("bed: unsupported store backend")
	ErrStoreConflict = errors.New("bed: store selection conflicts with existing identity")
	ErrStoreRecord   = errors.New("bed: cannot read store selection")
)

// Store selection survives normal evict and luggage GC. Those operations
// remove working data, not the Bed identity. Keeping this control record outside
// the Bed directory also prevents Stage-in from replacing the choice. It is
// local routing metadata, not a snapshot: a cross-carrier caller must repeat
// the desired choice. Purge removes it only after data deletion succeeds.
const storePolicyDir = ".bed-stores"

type storePolicyRecord struct {
	Store string `json:"store"`
}

func (m *Manager) validateStorePolicy(policy string) error {
	if policy != "" && policy != StoreDefault && policy != StoreNoop && policy != "auto" && policy != "s3" && policy != "cas" && policy != "pack" && policy != "tar" && policy != m.store.Name() {
		return ErrStoreInvalid
	}
	return nil
}

func (m *Manager) storePolicyPath(id string) string {
	return filepath.Join(m.root, storePolicyDir, id+".json")
}

func (m *Manager) readStorePolicy(id string) (string, bool, error) {
	raw, err := os.ReadFile(m.storePolicyPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("%w: %s: %v", ErrStoreRecord, id, err)
	}
	var record storePolicyRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return "", false, fmt.Errorf("%w: %s: %v", ErrStoreRecord, id, err)
	}
	if record.Store == "" || m.validateStorePolicy(record.Store) != nil {
		return "", false, fmt.Errorf("%w: invalid policy for %s", ErrStoreRecord, id)
	}
	return m.normalizedStorePolicy(record.Store), true, nil
}

func (m *Manager) resolveStorePolicy(id, requested string) (string, error) {
	requested = m.normalizedStorePolicy(requested)
	policy, exists, err := m.readStorePolicy(id)
	if err != nil {
		return "", err
	}
	if !exists {
		// Pre-selection versions have no control record. Existing local data was
		// created with the instance default; do not silently relabel it noop.
		if meta, local := loadMeta(filepath.Join(m.root, id)); local {
			policy, exists = m.normalizedStorePolicy(meta.Store), true
			if policy == "" {
				policy = m.store.Name()
			}
		}
	}
	if exists {
		if requested != "" && requested != policy {
			return "", ErrStoreConflict
		}
		return policy, nil
	}
	if requested != "" {
		return requested, nil
	}
	return m.store.Name(), nil
}

func (m *Manager) saveStorePolicy(id, policy string) error {
	filename := m.storePolicyPath(id)
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(storePolicyRecord{Store: policy})
	if err != nil {
		return err
	}
	return osx.WriteFileAtomic(filename, raw, 0o600)
}

func (m *Manager) storeForPolicy(ctx context.Context, policy string) (store.Store, error) {
	return store.Select(ctx, m.store, policy)
}

func (m *Manager) normalizedStorePolicy(policy string) string {
	if policy == StoreDefault {
		return m.store.Name()
	}
	if policy == "cas" {
		return "s3"
	}
	return policy
}

func storePolicyMatches(requested, recorded string) error {
	if requested != "" && requested != recorded {
		return ErrStoreConflict
	}
	return nil
}

// StoreName reports this Bed's effective backend; the instance name remains
// its default and cannot describe a mixed set of Beds.
func (b *Bed) StoreName() string { return b.store.Name() }
