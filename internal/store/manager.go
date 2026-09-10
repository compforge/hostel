package store

import (
	"context"
	"fmt"
	"sync"
)

// Kind is the canonical synchronization policy saved on a Bed.
type Kind string

const (
	KindNoop Kind = "noop"
	KindAuto Kind = "auto"
	KindCAS  Kind = "cas"
	KindPack Kind = "pack"
	KindTar  Kind = "tar"
)

// Manager is the daemon-wide persistence component. Beds carry only a Kind;
// this component owns shared stores, their clients, and the sync controller.
type Manager struct {
	defaultKind   Kind
	cfg           Config
	mu            sync.Mutex
	stores        map[Kind]Store
	syncRequested chan struct{}
}

func NewManager(ctx context.Context, cfg Config) (*Manager, error) {
	s := NewManagerWithStores(Noop{})
	s.cfg = cfg
	requested := cfg.Kind
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

// NewManagerWithStores assembles a Manager from shared implementation instances. The first
// implementation supplies the default; additional stores can be selected per Bed.
func NewManagerWithStores(defaultStore Store, additional ...Store) *Manager {
	s := &Manager{defaultKind: defaultStore.Name(), stores: map[Kind]Store{KindNoop: Noop{}}, syncRequested: make(chan struct{}, 1)}
	s.stores[defaultStore.Name()] = defaultStore
	for _, implementation := range additional {
		s.stores[implementation.Name()] = implementation
	}
	return s
}

func (s *Manager) DefaultKind() Kind { return s.defaultKind }

// Resolve applies the default only to omitted input and validates API/env
// input before the selection is recorded on the Bed. Configuration failures
// surface before accepting a Bed that could not use its requested implementation.
func (s *Manager) Resolve(ctx context.Context, requested string) (Kind, error) {
	if requested == "" {
		return s.defaultKind, nil
	}
	kind := Kind(requested)
	switch kind {
	case KindNoop, KindAuto, KindCAS, KindPack, KindTar:
	default:
		return "", fmt.Errorf("store: unknown store kind %q", requested)
	}
	implementation, err := s.forKind(ctx, kind)
	if err != nil {
		return "", err
	}
	return implementation.Name(), nil
}

// forKind returns the shared Store implementation for a resolved synchronization kind.
func (s *Manager) forKind(ctx context.Context, kind Kind) (Store, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if implementation := s.stores[kind]; implementation != nil {
		return implementation, nil
	}
	switch kind {
	case KindAuto:
		if s.cfg.Bucket == "" {
			return s.stores[KindNoop], nil
		}
		if s.cfg.AutoPackFileThreshold < 0 {
			return nil, fmt.Errorf("store: auto pack file threshold must be non-negative")
		}
	case KindCAS, KindPack, KindTar:
	default:
		return nil, fmt.Errorf("store: unknown store kind %q", kind)
	}
	if s.cfg.Bucket == "" {
		return s.stores[KindNoop], nil
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
	s.stores[KindAuto] = automatic
	for _, kind := range []Kind{KindCAS, KindPack, KindTar} {
		s.stores[kind] = automatic.withKind(kind)
	}
	return s.stores[kind], nil
}

func (s *Manager) Stat(ctx context.Context, kind Kind, bedID string) (*SnapshotInfo, error) {
	implementation, err := s.forKind(ctx, kind)
	if err != nil {
		return nil, err
	}
	return implementation.Stat(ctx, bedID)
}

func (s *Manager) Persist(ctx context.Context, kind Kind, bedID, dir string, generation int64) error {
	implementation, err := s.forKind(ctx, kind)
	if err != nil {
		return err
	}
	return implementation.Persist(ctx, bedID, dir, generation)
}

func (s *Manager) Delete(ctx context.Context, kind Kind, bedID string) error {
	implementation, err := s.forKind(ctx, kind)
	if err != nil {
		return err
	}
	return implementation.Delete(ctx, bedID)
}

func (s *Manager) StageInBedFS(ctx context.Context, kind Kind, request StageInRequest) (StageInResult, error) {
	implementation, err := s.forKind(ctx, kind)
	if err != nil {
		return StageInResult{}, err
	}
	return StageInBedFS(ctx, implementation, request)
}
