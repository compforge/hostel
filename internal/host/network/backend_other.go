//go:build !linux

package network

import (
	"context"

	hostel "github.com/qiankunli/hostel/internal"
)

func probeBackend(context.Context, ...*PortManager) (backend, Probe) {
	return nil, Probe{Cause: hostel.CauseCarrier, Stage: "platform", Error: "per-allocation network namespaces require Linux"}
}
