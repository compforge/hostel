package network

import "context"

// PolicyLease owns enforcement independently of namespace allocation. A failed
// Acquire may return a lease whose cleanup must succeed before identity reuse.
type PolicyLease interface {
	Update(context.Context, PolicyMutation) (PolicyStatus, error)
	Close(context.Context) error
}

type UIDPolicyProvider interface {
	Acquire(context.Context, string, uint32) (PolicyLease, error)
	Close(context.Context) error
}
