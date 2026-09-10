package store

import (
	"context"
	"fmt"
	"sync"
)

// BackendKind is the canonical persistence selection saved on a Bed.
type BackendKind string

const (
	BackendNoop BackendKind = "noop"
	BackendAuto BackendKind = "auto"
	BackendS3   BackendKind = "s3"
	BackendPack BackendKind = "pack"
	BackendTar  BackendKind = "tar"
)

// Store is the daemon-wide persistence component. Beds carry only a BackendKind;
// this component owns shared backends, their clients, and the sync controller.
type Store struct {
	defaultKind   BackendKind
	cfg           Config
	mu            sync.Mutex
	backends      map[BackendKind]Backend
	syncRequested chan struct{}
}

func New(ctx context.Context, cfg Config) (*Store, error) {
	s := NewWithBackends(Noop{})
	s.cfg = cfg
	requested := cfg.Backend
	if requested == "" {
		requested = string(BackendAuto)
	}
	kind, err := s.Resolve(ctx, requested)
	if err != nil {
		return nil, err
	}
	s.defaultKind = kind
	return s, nil
}

// NewWithBackends assembles a Store from shared backend instances. The first
// backend supplies the default; additional backends can be selected per Bed.
func NewWithBackends(defaultBackend Backend, additional ...Backend) *Store {
	s := &Store{defaultKind: defaultBackend.Name(), backends: map[BackendKind]Backend{BackendNoop: Noop{}}, syncRequested: make(chan struct{}, 1)}
	s.backends[defaultBackend.Name()] = defaultBackend
	for _, backend := range additional {
		s.backends[backend.Name()] = backend
	}
	return s
}

func (s *Store) DefaultKind() BackendKind { return s.defaultKind }

// Resolve applies the default only to omitted input and canonicalizes API/env
// aliases before the selection is recorded on the Bed. Configuration failures
// surface before accepting a Bed that could not use its requested backend.
func (s *Store) Resolve(ctx context.Context, requested string) (BackendKind, error) {
	if requested == "" {
		return s.defaultKind, nil
	}
	if requested == "cas" {
		requested = string(BackendS3)
	}
	kind := BackendKind(requested)
	switch kind {
	case BackendNoop, BackendAuto, BackendS3, BackendPack, BackendTar:
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
func (s *Store) backend(ctx context.Context, kind BackendKind) (Backend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if backend := s.backends[kind]; backend != nil {
		return backend, nil
	}
	switch kind {
	case BackendAuto:
		if s.cfg.Bucket == "" {
			return s.backends[BackendNoop], nil
		}
		if s.cfg.AutoPackFileThreshold < 0 {
			return nil, fmt.Errorf("store: auto pack file threshold must be non-negative")
		}
	case BackendS3, BackendPack, BackendTar:
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
	s.backends[BackendAuto] = automatic
	s.backends[BackendS3] = automatic.cas
	s.backends[BackendPack] = automatic.pack
	s.backends[BackendTar] = automatic.tar
	return s.backends[kind], nil
}

func (s *Store) Stat(ctx context.Context, kind BackendKind, bedID string) (*SnapshotInfo, error) {
	backend, err := s.backend(ctx, kind)
	if err != nil {
		return nil, err
	}
	return backend.Stat(ctx, bedID)
}

func (s *Store) Persist(ctx context.Context, kind BackendKind, bedID, dir string, generation int64) error {
	backend, err := s.backend(ctx, kind)
	if err != nil {
		return err
	}
	return backend.Persist(ctx, bedID, dir, generation)
}

func (s *Store) Delete(ctx context.Context, kind BackendKind, bedID string) error {
	backend, err := s.backend(ctx, kind)
	if err != nil {
		return err
	}
	return backend.Delete(ctx, bedID)
}

func (s *Store) StageInBedFS(ctx context.Context, kind BackendKind, request StageInRequest) (StageInResult, error) {
	backend, err := s.backend(ctx, kind)
	if err != nil {
		return StageInResult{}, err
	}
	return StageInBedFS(ctx, backend, request)
}
