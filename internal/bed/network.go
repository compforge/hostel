package bed

import (
	"context"
	"net"
	"net/url"
	"os/exec"

	"github.com/qiankunli/hostel/internal/executor"
	"github.com/qiankunli/hostel/internal/network"
)

// SetNetworkManager is called once before serving. Network ownership follows
// resident Bed lifetime, so replacing an Executor does not replace its netns.
func (m *Manager) SetNetworkManager(manager *network.Manager) { m.network = manager }
func (m *Manager) NetworkReport() network.Report              { return m.network.Report() }

func (m *Manager) networkExecutorFactory() executor.Factory {
	if !m.network.Report().Enabled {
		return m.executorFactory
	}
	return networkFactory{Factory: m.executorFactory, manager: m.network}
}

type networkFactory struct {
	executor.Factory
	manager *network.Manager
}

func (f networkFactory) Create(ctx context.Context, bedID string) (executor.Executor, error) {
	e, err := f.Factory.Create(ctx, bedID)
	if err != nil {
		return nil, err
	}
	return networkExecutor{Executor: e, manager: f.manager}, nil
}

type networkExecutor struct {
	executor.Executor
	manager *network.Manager
}

func (e networkExecutor) Start(ctx context.Context, id string, cmd *exec.Cmd) (executor.Process, error) {
	if err := e.manager.Wrap(e.BedID(), cmd); err != nil {
		return nil, err
	}
	return e.Executor.Start(ctx, id, cmd)
}

// The shared browser remains outside the Bed namespace. Its Bed-scoped API
// is reached via the veth gateway, never the Bed's now-private loopback.
func (m *Manager) networkEndpoint(bedID, endpoint string) string {
	gateway := m.network.Gateway(bedID)
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
