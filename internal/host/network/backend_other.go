//go:build !linux

package network

import "context"

func probeBackend(context.Context) (backend, Probe) {
	return nil, Probe{Stage: "platform", Error: "per-allocation network namespaces require Linux"}
}
