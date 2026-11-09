package manager

import (
	"context"
	"net"
	"net/url"
	"path/filepath"

	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/protocol"
)

func (m *Manager) SetNetworkManager(manager *network.Manager) {
	if manager == nil {
		manager = network.WithProvider(nil, m.owners.Network)
	}
	manager.SetStateDirectory(filepath.Join(m.root, ".network"))
	manager.SetStatusWriter(m.owners.Network)
	manager.SetPortMappings(m.portMappings)
	manager.SetDedicatedUIDs(m.privileges.Status().BedUser.Strategy == "per_bed")
	m.network = manager
}
func (m *Manager) NetworkReport() network.Status { return m.network.Status() }

// The shared browser remains outside the Bed namespace. Its Bed-scoped API
// is reached via the veth gateway, never the Bed's now-private loopback.
func (m *Manager) networkEndpoint(b *managedBed, endpoint string) string {
	gateway := b.environment.Gateway()
	if gateway == "" {
		return endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && ip.IsLoopback() {
		u.Host = net.JoinHostPort(gateway, u.Port())
	}
	return u.String()
}

// NetworkPolicy consumes an existing Bed; the HTTP layer holds an operation
// reference so eviction cannot destroy its namespace during a kernel update.
func (m *Manager) NetworkPolicy(ctx context.Context, b *managedBed, mutation network.PolicyMutation) (protocol.EgressStatus, error) {
	return m.network.UpdatePolicy(ctx, b.Bed, mutation)
}
