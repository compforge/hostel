//go:build !linux

package network

import "context"

// No persistent network mechanisms are installed on these platforms.
func OpenAllocationStore(context.Context, string) (*AllocationStore, error) { return nil, nil }
