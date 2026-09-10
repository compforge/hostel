package store

import (
	"context"
	"fmt"
	"sync"
)

// Kind is the canonical persistence selection saved on a Bed.
type Kind string

const (
	KindNoop Kind = "noop"
	KindAuto Kind = "auto"
	KindS3   Kind = "s3"
	KindPack Kind = "pack"
	KindTar  Kind = "tar"
)

// Manager is the daemon-wide persistence component. Beds carry only a Kind;
// this component owns shared backends, their clients, and the sync controller.
type Manager struct {
	defaultKind   Kind
	cfg           Config
	mu            sync.Mutex
	backends      map[Kind]Store
	syncRequested chan struct{}
}

func NewManager(ctx context.Context, cfg Config) (*Manager, error) {
	s := NewManagerWithStores(Noop{})
	s.cfg = cfg
	requested := cfg.Backend
	if requested == "" {
		requested = string(KindAuto)
	}
	kind, err := s.Resolve(ctx, requested)
	if err != nil {
		return nil, err
	}
	s.defaultKind = kind
	return s, nil
}

// NewManagerWithStores assembles a Manager from shared backend instances. The first
// backend supplies the default; additional backends can be selected per Bed.
func NewManagerWithStores(defaultBackend Store, additional ...Store) *Manager {
	s := &Manager{defaultKind: defaultBackend.Name(), backends: map[Kind]Store{KindNoop: Noop{}}, syncRequested: make(chan struct{}, 1)}
	s.backends[defaultBackend.Name()] = defaultBackend
	for _, backend := range additional {
		s.backends[backend.Name()] = backend
	}
	return s
}

func (s *Manager) DefaultKind() Kind { return s.defaultKind }

// Resolve applies the default only to omitted input and canonicalizes API/env
// aliases before the selection is recorded on the Bed. Configuration failures
// surface before accepting a Bed that could not use its requested backend.
func (s *Manager) Resolve(ctx context.Context, requested string) (Kind, error) {
	if requested == "" {
		return s.defaultKind, nil
	}
	if requested == "cas" {
		requested = string(KindS3)
	}
	kind := Kind(requested)
	switch kind {
	case KindNoop, KindAuto, KindS3, KindPack, KindTar:
	default:
		return "", fmt.Errorf("store: unknown backend %q", requested)
	}
	backend, err := s.backend(ctx, kind)
	if err != nil {
		return "", err
	}
	return backend.Name(), nil
}

// backend returns the shared implementation for a Bed's already resolved kind.
func (s *Manager) backend(ctx context.Context, kind Kind) (Store, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if backend := s.backends[kind]; backend != nil {
		return backend, nil
	}
	switch kind {
	case KindAuto:
		if s.cfg.Bucket == "" {
			return s.backends[KindNoop], nil
		}
		if s.cfg.AutoPackFileThreshold < 0 {
			return nil, fmt.Errorf("store: auto pack file threshold must be non-negative")
		}
	case KindS3, KindPack, KindTar:
	default:
		return nil, fmt.Errorf("store: unknown backend %q", kind)
	}
	if s.cfg.Bucket == "" {
		return nil, fmt.Errorf("store: %s backend requires a bucket", kind)
	}
	// A noop default leaves S3 lazy. All durable formats share this one client.
	obj, err := newS3Obj(ctx, s.cfg)
	if err != nil {
		return nil, err
	}
	filter, err := newSnapshotFilter(s.cfg.PersistedPaths)
	if err != nil {
		return nil, err
	}
	automatic := newAutoStore(obj, s.cfg.Prefix, s.cfg.AutoPackFileThreshold, filter)
	s.backends[KindAuto] = automatic
	s.backends[KindS3] = automatic.cas
	s.backends[KindPack] = automatic.pack
	s.backends[KindTar] = automatic.tar
	return s.backends[kind], nil
}

func (s *Manager) Stat(ctx context.Context, kind Kind, bedID string) (*SnapshotInfo, error) {
	backend, err := s.backend(ctx, kind)
	if err != nil {
		return nil, err
	}
	return backend.Stat(ctx, bedID)
}

func (s *Manager) Persist(ctx context.Context, kind Kind, bedID, dir string, generation int64) error {
	backend, err := s.backend(ctx, kind)
	if err != nil {
		return err
	}
	return backend.Persist(ctx, bedID, dir, generation)
}

func (s *Manager) Delete(ctx context.Context, kind Kind, bedID string) error {
	backend, err := s.backend(ctx, kind)
	if err != nil {
		return err
	}
	return backend.Delete(ctx, bedID)
}

func (s *Manager) StageInBedFS(ctx context.Context, kind Kind, request StageInRequest) (StageInResult, error) {
	backend, err := s.backend(ctx, kind)
	if err != nil {
		return StageInResult{}, err
	}
	return StageInBedFS(ctx, backend, request)
}
