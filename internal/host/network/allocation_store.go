package network

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/qiankunli/go-stdx/osx"
)

// AllocationStore records intent before kernel mutation. Its directory belongs
// to one runtime instance and is outside portable Bed data and snapshots.
// +spec=`Keep exact allocation ownership until cleanup succeeds; failed recovery prevents new admission.`
// +link:doc=docs/network.md
type AllocationStore struct {
	mu            sync.Mutex
	directory     string
	lock          *os.File
	carrier, boot string
}

type allocationRecord struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"`
	Key     string `json:"owner"`
	Name    string `json:"name"`
	Link    string `json:"link,omitempty"`
	Peer    string `json:"peer,omitempty"`
	UID     uint32 `json:"uid,omitempty"`
	Carrier string `json:"carrier"`
	Boot    string `json:"boot"`
}

func (s *AllocationStore) Add(r allocationRecord) (*allocationRecord, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return nil, fmt.Errorf("network allocation store closed")
	}
	r.Version, r.Carrier, r.Boot = 1, s.carrier, s.boot
	if err := r.validate(); err != nil {
		return nil, err
	}
	path := filepath.Join(s.directory, r.Name+".json")
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return nil, fmt.Errorf("allocation record %s already exists or cannot be inspected: %v", r.Name, err)
	}
	data, err := json.Marshal(r)
	if err == nil {
		err = osx.WriteFileAtomic(path, data, 0600)
	}
	if err != nil {
		return nil, fmt.Errorf("record network allocation %s: %w", r.Name, err)
	}
	return &r, nil
}

func (s *AllocationStore) Remove(r *allocationRecord) error {
	if s == nil || r == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return fmt.Errorf("network allocation store closed")
	}
	err := os.Remove(filepath.Join(s.directory, r.Name+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (r allocationRecord) validate() error {
	valid := func(s string, prefix string, n int) bool {
		if len(s) != len(prefix)+n || s[:len(prefix)] != prefix {
			return false
		}
		for _, c := range s[len(prefix):] {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
		return true
	}
	if r.Version != 1 || r.Key == "" || r.Carrier == "" || r.Boot == "" {
		return fmt.Errorf("invalid network allocation identity %q", r.Name)
	}
	switch r.Kind {
	case "uid":
		if valid(r.Name, "hu_", 16) && r.UID > 0 && r.Link == "" && r.Peer == "" {
			return nil
		}
	case "netns":
		if valid(r.Name, "hn", 12) && r.Link == r.Name && valid(r.Peer, "np", 8) && r.UID == 0 {
			return nil
		}
	}
	return fmt.Errorf("invalid network allocation record %q kind=%q", r.Name, r.Kind)
}

// Close releases the instance lock only after the caller has closed its network
// allocations. Failed cleanup retains both the record and this lock for retry.
func (s *AllocationStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return nil
	}
	err := s.lock.Close()
	if err == nil {
		s.lock = nil
	}
	return err
}
