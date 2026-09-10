package store

import (
	"context"
	"fmt"
	"sync"
)

// Selection keeps the configured instance default and resolves per-Bed
// overrides. All formats share one object client and its connection pool.
type Selection struct {
	Store
	mu       sync.Mutex
	cfg      Config
	backends map[string]Store
}

func NewSelection(ctx context.Context, cfg Config) (*Selection, error) {
	defaultStore, err := New(ctx, cfg)
	if err != nil {
		return nil, err
	}
	s := &Selection{Store: defaultStore, cfg: cfg, backends: map[string]Store{"noop": Noop{}}}
	var obj objAPI
	switch backend := defaultStore.(type) {
	case *autoStore:
		obj = backend.cas.obj
	case *casStore:
		obj = backend.obj
	case *packStore:
		obj = backend.obj
	case *tarStore:
		obj = backend.obj
	}
	if obj != nil {
		if err := s.configure(obj); err != nil {
			return nil, err
		}
		s.backends[defaultStore.Name()] = defaultStore
	}
	return s, nil
}

func (s *Selection) configure(obj objAPI) error {
	filter, err := newSnapshotFilter(s.cfg.PersistedPaths)
	if err != nil {
		return err
	}
	automatic := newAutoStore(obj, s.cfg.Prefix, s.cfg.AutoPackFileThreshold, filter)
	s.backends["auto"] = automatic
	s.backends["s3"] = automatic.cas
	s.backends["pack"] = automatic.pack
	s.backends["tar"] = automatic.tar
	return nil
}

func (s *Selection) selectBackend(ctx context.Context, name string) (Store, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if backend := s.backends[name]; backend != nil {
		return backend, nil
	}
	if name != "auto" && name != "s3" && name != "pack" && name != "tar" {
		return nil, fmt.Errorf("store: unknown backend %q", name)
	}
	if s.cfg.Bucket == "" {
		if name == "auto" {
			return Noop{}, nil
		}
		return nil, fmt.Errorf("store: %s backend requires a bucket", name)
	}
	// A noop instance can still configure S3 without activating it. Construct
	// the shared client only when a Bed explicitly requests a durable backend.
	obj, err := newS3Obj(ctx, s.cfg)
	if err != nil {
		return nil, err
	}
	if err := s.configure(obj); err != nil {
		return nil, err
	}
	return s.backends[name], nil
}

// Select resolves an API choice against the configured default. The selected
// Store.Name is the effective value recorded on the Bed (cas normalizes to s3).
func Select(ctx context.Context, configured Store, name string) (Store, error) {
	if name == "" || name == "default" {
		return configured, nil
	}
	if name == "cas" {
		name = "s3"
	}
	if configured.Name() == name {
		return configured, nil
	}
	if name == "noop" {
		return Noop{}, nil
	}
	if selection, ok := configured.(*Selection); ok {
		return selection.selectBackend(ctx, name)
	}
	return nil, fmt.Errorf("store: backend %q is not configured", name)
}
