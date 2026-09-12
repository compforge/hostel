// Package bed defines Hostel's shared Bed model and component contracts.
// It has no dependency on HTTP or domain implementations.
package bed

import (
	"sync"
	"time"

	"github.com/qiankunli/go-stdx/randx"
)

// ID identifies a local Bed lifetime. Names are caller-owned routing keys;
// IDs survive daemon restart with local data, but never travel in snapshots.
type ID string

func (id ID) String() string { return string(id) }

// Bed is the shared identity seen by every domain manager. Resource handles
// belong to those managers, not to this model.
// +spec=`Spec is desired state; each Status section has exactly one domain writer.`
type Bed struct {
	ID     ID
	Name   string
	mu     sync.RWMutex
	spec   Spec
	status Status
}

type Spec struct {
	Dir             string
	CreatedAt       time.Time
	Sync            SyncKind
	NetworkPolicy   *NetworkPolicy
	RecoveryDirs    []string
	LocalPresent    bool
	LocalGeneration int64
}

type NetworkRule struct {
	Action string `json:"action"`
	Target string `json:"target"`
}
type NetworkPolicy struct {
	DefaultAction string        `json:"defaultAction"`
	Egress        []NetworkRule `json:"egress"`
}

func clonePolicy(p *NetworkPolicy) *NetworkPolicy {
	if p == nil {
		return nil
	}
	c := *p
	c.Egress = append([]NetworkRule(nil), p.Egress...)
	return &c
}
func cloneSpec(s Spec) Spec {
	s.NetworkPolicy = clonePolicy(s.NetworkPolicy)
	s.RecoveryDirs = append([]string(nil), s.RecoveryDirs...)
	return s
}

// New creates or recovers a local identity. Reinitializing a retained Bed reuses
// this object; resource managers own replaceable runtime allocations.
func New(name string, id ID, spec Spec) *Bed {
	if id == "" {
		id = ID("bed-" + randx.Hex(16))
	}
	return &Bed{ID: id, Name: name, spec: cloneSpec(spec)}
}
func (b *Bed) Spec() Spec     { b.mu.RLock(); defer b.mu.RUnlock(); return cloneSpec(b.spec) }
func (b *Bed) Status() Status { b.mu.RLock(); defer b.mu.RUnlock(); return cloneStatus(b.status) }

// SpecWriter is given only to Bed Manager by the composition root.
type SpecWriter struct{}

func (SpecWriter) Update(b *Bed, change func(*Spec)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := cloneSpec(b.spec)
	change(&s)
	b.spec = cloneSpec(s)
}
