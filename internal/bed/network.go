package bed

import (
	"context"
	"net"
	"net/url"

	"github.com/qiankunli/hostel/internal/network"
)

// bedNetwork supplies allocations; a Bed retains the returned handle.
type bedNetwork interface {
	Report() network.Report
	Acquire(context.Context, string) (network.Attachment, error)
	NetworkPolicy(context.Context, string, network.PolicyMutation) (network.PolicyStatus, error)
	Close(context.Context) error
}

func (m *Manager) SetNetworkManager(manager *network.Manager) { m.network = manager }
func (m *Manager) NetworkReport() network.Report              { return m.network.Report() }

// The shared browser remains outside the Bed namespace. Its Bed-scoped API
// is reached via the veth gateway, never the Bed's now-private loopback.
func (m *Manager) networkEndpoint(b *Bed, endpoint string) string {
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
func (m *Manager) NetworkPolicy(ctx context.Context, b *Bed, mutation network.PolicyMutation) (network.PolicyStatus, error) {
	return m.network.NetworkPolicy(ctx, b.ID, mutation)
}
