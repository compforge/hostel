package store

import (
	"context"
	"fmt"
	"sync"

	"github.com/qiankunli/hostel/internal/store/backend"
	storesync "github.com/qiankunli/hostel/internal/store/sync"
)

// Manager is the daemon-wide persistence component. Beds carry only a SyncKind;
// this component owns shared stores, their clients, and the sync controller.
type Manager struct {
	defaultSync   SyncKind
	cfg           Config
	mu            sync.Mutex
	stores        map[SyncKind]Store
	syncRequested chan struct{}
	remote        *backend.S3
	transfers     *transferRegistry
	restic        *storesync.Restic
}

// Report is the safe operator view of persistence and explicit transfers. It
// intentionally omits remote coordinates and credentials.
type Report struct {
	DefaultSync         SyncKind `json:"default_sync"`
	Backend             string   `json:"backend"`
	TransfersConfigured bool     `json:"transfers_configured"`
}

func (s *Manager) Diagnostics() Report {
	if s == nil {
		return Report{Backend: "none"}
	}
	backend := "none"
	if s.cfg.Bucket != "" {
		backend = "s3"
	}
	return Report{DefaultSync: s.defaultSync, Backend: backend, TransfersConfigured: s.cfg.Bucket != ""}
}

func NewManager(ctx context.Context, cfg Config) (*Manager, error) {
	s := NewManagerWithStores(Noop{})
	s.cfg = cfg
	s.restic = storesync.NewRestic(storesync.ResticConfig{Config: cfg.remoteConfig(), Binary: cfg.ResticBinary, Password: cfg.ResticPassword})
	requested := cfg.Sync
	if requested == "" {
		requested = string(SyncAuto)
	}
	kind, err := s.Resolve(ctx, requested)
	if err != nil {
		return nil, err
	}
	s.defaultSync = kind
	return s, nil
}

// NewManagerWithStores assembles a Manager from shared implementation instances. The first
// implementation supplies the default; additional stores can be selected per Bed.
func NewManagerWithStores(defaultStore Store, additional ...Store) *Manager {
	s := &Manager{defaultSync: defaultStore.Name(), stores: map[SyncKind]Store{SyncNoop: Noop{}}, syncRequested: make(chan struct{}, 1), transfers: newTransferRegistry()}
	s.stores[defaultStore.Name()] = defaultStore
	for _, implementation := range additional {
		s.stores[implementation.Name()] = implementation
	}
	return s
}

func (s *Manager) DefaultSync() SyncKind { return s.defaultSync }

// Resolve applies the default only to omitted input and validates API/env
// input before the selection is recorded on the Bed. Configuration failures
// surface before accepting a Bed that could not use its requested implementation.
func (s *Manager) Resolve(ctx context.Context, requested string) (SyncKind, error) {
	if requested == "" {
		return s.defaultSync, nil
	}
	kind := SyncKind(requested)
	switch kind {
	case SyncNoop, SyncAuto, SyncCAS, SyncPack, SyncTar, SyncRestic:
	default:
		return "", fmt.Errorf("store: unknown sync kind %q", requested)
	}
	if kind == SyncRestic && s.cfg.Bucket != "" {
		if err := s.restic.Available(ctx); err != nil {
			return "", err
		}
	}
	implementation, err := s.forSync(ctx, kind)
	if err != nil {
		return "", err
	}
	return implementation.Name(), nil
}

// forSync returns the shared Store implementation for a resolved synchronization kind.
func (s *Manager) forSync(ctx context.Context, kind SyncKind) (Store, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if implementation := s.stores[kind]; implementation != nil {
		return implementation, nil
	}
	switch kind {
	case SyncAuto:
		if s.cfg.Bucket == "" {
			return s.stores[SyncNoop], nil
		}
		if s.cfg.AutoPackFileThreshold < 0 {
			return nil, fmt.Errorf("store: auto pack file threshold must be non-negative")
		}
	case SyncCAS, SyncPack, SyncTar, SyncRestic:
	default:
		return nil, fmt.Errorf("store: unknown sync kind %q", kind)
	}
	if s.cfg.Bucket == "" {
		return s.stores[SyncNoop], nil
	}
	// A noop default leaves S3 lazy. All durable formats share this one client.
	obj, err := s.remoteLocked(ctx)
	if err != nil {
		return nil, err
	}
	automatic, err := storesync.NewAuto(obj, s.cfg.Prefix, s.cfg.AutoPackFileThreshold, s.cfg.PersistedPaths, s.restic)
	if err != nil {
		return nil, err
	}
	s.stores[SyncAuto] = automatic
	for _, kind := range []SyncKind{SyncCAS, SyncPack, SyncTar, SyncRestic} {
		s.stores[kind] = automatic.WithKind(kind)
	}
	return s.stores[kind], nil
}

func (s *Manager) Stat(ctx context.Context, kind SyncKind, bedID string) (*SnapshotInfo, error) {
	implementation, err := s.forSync(ctx, kind)
	if err != nil {
		return nil, err
	}
	return implementation.Stat(ctx, bedID)
}

func (s *Manager) Persist(ctx context.Context, kind SyncKind, bedID, dir string, generation int64) error {
	implementation, err := s.forSync(ctx, kind)
	if err != nil {
		return err
	}
	return implementation.Persist(ctx, bedID, dir, generation)
}

func (s *Manager) Delete(ctx context.Context, kind SyncKind, bedID string) error {
	implementation, err := s.forSync(ctx, kind)
	if err != nil {
		return err
	}
	return implementation.Delete(ctx, bedID)
}

func (s *Manager) StageInBedFS(ctx context.Context, kind SyncKind, request StageInRequest) (StageInResult, error) {
	implementation, err := s.forSync(ctx, kind)
	if err != nil {
		return StageInResult{}, err
	}
	return StageInBedFS(ctx, implementation, request)
}

// remoteLocked shares the same S3 client even when the default policy is noop.
// The caller holds s.mu; connection setup never changes a Bed's policy.
func (s *Manager) remoteLocked(ctx context.Context) (*backend.S3, error) {
	if s.remote != nil {
		return s.remote, nil
	}
	if s.cfg.Bucket == "" {
		return nil, ErrTransferUnavailable
	}
	remote, err := backend.NewS3(ctx, s.cfg.remoteConfig())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransferUnavailable, err)
	}
	s.remote = remote
	return s.remote, nil
}
