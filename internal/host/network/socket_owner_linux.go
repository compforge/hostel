//go:build linux

package network

import "context"

// InspectTCPListener reads tables from service group members, not the daemon's
// netns. A namespace identifies a Bed, not an individual service within it.
func InspectTCPListener(ctx context.Context, group int, address string) (ListenerInspection, error) {
	return inspectProcListener(ctx, "/proc", group, address)
}
