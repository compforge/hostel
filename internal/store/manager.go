package store

import (
	"context"
	"fmt"
	"sync"
)

// Manager is the daemon-wide persistence component. Beds carry only a SyncKind;
// this component owns shared stores, their clients, and the sync controller.
type Manager struct {
	defaultSync   SyncKind
	cfg           Config
	mu            sync.Mutex
	stores        map[SyncKind]Store
	syncRequested chan struct{}
	remote        *s3obj
	transfers     *transferRegistry
	restic        *resticCommand
}

func NewManager(ctx context.Context, cfg Config) (*Manager, error) {
	s := NewManagerWithStores(Noop{})
	s.cfg = cfg
	s.restic = newResticCommand(cfg)
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
		if err := s.restic.available(ctx); err != nil {
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
	filter, err := newSnapshotFilter(s.cfg.PersistedPaths)
	if err != nil {
		return nil, err
	}
	automatic := newAutoStore(obj, s.cfg.Prefix, s.cfg.AutoPackFileThreshold, filter)
	automatic.restic = &resticStore{obj: obj, prefix: s.cfg.Prefix, command: s.restic, filter: filter}
	s.stores[SyncAuto] = automatic
	for _, kind := range []SyncKind{SyncCAS, SyncPack, SyncTar, SyncRestic} {
		s.stores[kind] = automatic.withSync(kind)
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
func (s *Manager) remoteLocked(ctx context.Context) (*s3obj, error) {
	if s.remote != nil {
		return s.remote, nil
	}
	if s.cfg.Bucket == "" {
		return nil, ErrTransferUnavailable
	}
	client, err := newS3Client(ctx, s.cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransferUnavailable, err)
	}
	s.remote = &s3obj{client: client, bucket: s.cfg.Bucket}
	return s.remote, nil
}
