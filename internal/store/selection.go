package store

import (
	"context"
	"fmt"
	"sync"
)

// backends owns the instance default and one shared S3 client for all Beds.
// A noop default leaves S3 initialization lazy, so credentials and connectivity
// are only required when a Bed actually selects a durable backend.
type backends struct {
	Store
	cfg    Config
	mu     sync.Mutex
	remote *autoStore
}

func (s *backends) selectBackend(ctx context.Context, name string) (Store, error) {
	switch name {
	case "noop":
		return Noop{}, nil
	case "", "auto":
		name = "auto"
		if s.cfg.Bucket == "" {
			return Noop{}, nil
		}
		if s.cfg.AutoPackFileThreshold < 0 {
			return nil, fmt.Errorf("store: auto pack file threshold must be non-negative")
		}
	case "s3", "cas", "pack", "tar":
		if s.cfg.Bucket == "" {
			return nil, fmt.Errorf("store: %s backend requires a bucket", name)
		}
	default:
		return nil, fmt.Errorf("store: unknown backend %q", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remote == nil {
		obj, err := newS3Obj(ctx, s.cfg)
		if err != nil {
			return nil, err
		}
		filter, err := newSnapshotFilter(s.cfg.PersistedPaths)
		if err != nil {
			return nil, err
		}
		s.remote = newAutoStore(obj, s.cfg.Prefix, s.cfg.AutoPackFileThreshold, filter)
	}
	switch name {
	case "s3", "cas":
		return s.remote.cas, nil
	case "pack":
		return s.remote.pack, nil
	case "tar":
		return s.remote.tar, nil
	default:
		return s.remote, nil
	}
}

// Select resolves an explicit backend against the instance default. A Bed
// stores the returned backend; aliases and configuration stay inside Store.
func Select(ctx context.Context, configured Store, name string) (Store, error) {
	if name == "" || name == configured.Name() {
		return configured, nil
	}
	if name == "noop" {
		return Noop{}, nil
	}
	if s, ok := configured.(*backends); ok {
		return s.selectBackend(ctx, name)
	}
	return nil, fmt.Errorf("store: backend %q is not configured", name)
}
