//go:build !linux

package network

import "context"

func probeBackend(context.Context, ...*PortManager) (backend, Probe) {
	return nil, Probe{Stage: "platform", Error: "per-allocation network namespaces require Linux"}
}
