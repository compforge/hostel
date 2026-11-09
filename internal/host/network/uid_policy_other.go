//go:build !linux

package network

import (
	"context"
	hostel "github.com/qiankunli/hostel/internal"
)

func ProbeUIDPolicy(context.Context, *PortManager) (UIDPolicyProvider, Probe) {
	return nil, Probe{Stage: "platform", Cause: hostel.CauseCarrier, Error: "UID policy requires Linux"}
}
