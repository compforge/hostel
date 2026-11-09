//go:build !linux

package network

import (
	"context"

	hostel "github.com/qiankunli/hostel/internal"
)

func probeBackend(ctx context.Context, ports ...*PortManager) (backend, Probe) {
	return probeBackendWithStore(ctx, nil, ports...)
}
func probeBackendWithStore(context.Context, *AllocationStore, ...*PortManager) (backend, Probe) {
	err := hostel.WrapError(hostel.ErrUnavailable, "per-allocation network namespaces require Linux", hostel.CauseCarrier, nil)
	return nil, Probe{Err: err, Cause: hostel.CauseCarrier, Stage: "platform", Error: err.Error()}
}
