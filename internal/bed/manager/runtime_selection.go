package manager

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/bed/privilege"
	"github.com/qiankunli/hostel/internal/bed/resource"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

type RuntimeConfig struct {
	Room       bed.RoomType
	Filesystem isolation.Config
	Privilege  privilege.Config
	Network    network.Config
	Resource   resource.Config
	Executor   executor.Config
}

type RuntimeSelection struct {
	Room     bed.RoomType
	Files    isolation.Isolator
	Identity privilege.Selection
	Network  network.Config
	Executor executor.Config
	Attempts []CombinationAttempt
}
type CombinationAttempt struct {
	Filesystem  string          `json:"filesystem"`
	ProcessView string          `json:"process_view"`
	Network     string          `json:"network"`
	Identity    privilege.Level `json:"identity"`
	Executor    string          `json:"executor"`
	Error       string          `json:"error,omitempty"`
}

func WithRuntimeSelection(s RuntimeSelection) ManagerOption {
	return func(m *Manager) {
		m.roomType = s.Room
		identity := s.Identity
		m.identitySelection = &identity
		m.bedUser = identity.User
		m.combinationAttempts = append([]CombinationAttempt(nil), s.Attempts...)
	}
}

// ResolveRuntime validates candidate environments before recovering real Bed
// identities. Failed attempts own isolated scratch data and must fully release
// allocations before another attempt can run.
// +rule=`Only startup selection may retry weaker optional combinations. Required policies and cleanup errors stop selection; live Bed environments are immutable.`
func ResolveRuntime(ctx context.Context, host hostfacts.Snapshot, root, shell string, cfg RuntimeConfig, ports *hostnetwork.PortManager) (RuntimeSelection, error) {
	return resolveRuntime(ctx, host, root, shell, cfg, ports, probeRuntime)
}

type runtimeProbe func(context.Context, hostfacts.Snapshot, string, string, RuntimeConfig, RuntimeSelection, *hostnetwork.PortManager) (CombinationAttempt, error)

func resolveRuntime(ctx context.Context, host hostfacts.Snapshot, root, shell string, cfg RuntimeConfig, ports *hostnetwork.PortManager, probe runtimeProbe) (RuntimeSelection, error) {
	room, err := bed.ParseRoomType(string(cfg.Room))
	if err != nil {
		return RuntimeSelection{}, err
	}
	cfg.Room = room
	files := cfg.Filesystem.ForRoom(room)
	netConfig := cfg.Network.ForRoom(room)
	if err := errors.Join(files.Validate(), netConfig.Validate(), cfg.Resource.Validate()); err != nil {
		return RuntimeSelection{}, err
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return RuntimeSelection{}, err
	}
	identity, err := privilege.Resolve(ctx, host, cfg.Privilege, cfg.Room, root)
	if err != nil {
		return RuntimeSelection{}, err
	}
	files.DedicatedIdentity = identity.Effective == privilege.Dedicated
	original := files
	selection := RuntimeSelection{Room: cfg.Room, Identity: identity, Network: netConfig}
	for {
		if err := ctx.Err(); err != nil {
			return selection, err
		}
		iso, err := isolation.Resolve(host, files, root)
		if err != nil {
			return selection, err
		}
		if err := ctx.Err(); err != nil {
			return selection, err
		}
		selection.Files, selection.Network = iso, netConfig
		attempt, fatalErr := probe(ctx, host, root, shell, cfg, selection, ports)
		selection.Attempts = append(selection.Attempts, attempt)
		if fatalErr != nil {
			return selection, fmt.Errorf("runtime probe: %w", fatalErr)
		}
		if attempt.Error == "" {
			selection.Executor = executor.Config{Backend: attempt.Executor}
			if attempt.Network == string(network.Shared) && netConfig.Level == network.Private {
				if next, ok := netConfig.WithoutOptionalNamespace("private network unavailable"); ok {
					selection.Network = next
				}
			}
			return selection, nil
		}
		log.Printf("hostel: runtime combination rejected filesystem=%s view=%s network=%s reason=%q", attempt.Filesystem, attempt.ProcessView, attempt.Network, attempt.Error)
		if next, ok := isolation.NextCombination(files, iso, attempt.Error); ok {
			files = next
			continue
		}
		if next, ok := netConfig.WithoutOptionalNamespace(attempt.Error); ok {
			netConfig, files = next, original
			continue
		}
		if next, ok := selection.Identity.WithoutDedicatedIdentity(attempt.Error); ok {
			selection.Identity = next
			original.DedicatedIdentity = false
			files, netConfig = original, cfg.Network.ForRoom(cfg.Room)
			continue
		}
		return selection, fmt.Errorf("no executable Bed combination: %s", attempt.Error)
	}
}

func probeRuntime(ctx context.Context, host hostfacts.Snapshot, root, shell string, cfg RuntimeConfig, selection RuntimeSelection, ports *hostnetwork.PortManager) (attempt CombinationAttempt, fatalErr error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	attempt.Filesystem = selection.Files.Name()
	attempt.Identity = selection.Identity.Effective
	if report, ok := selection.Files.(isolation.Report); ok {
		attempt.ProcessView = report.ProcessView().Mode
	}
	scratch, err := os.MkdirTemp(root, ".runtime-probe-")
	if err != nil {
		return attempt, err
	}
	// Foreign Bed users must traverse the scratch parent just like the actual root.
	if err := os.Chmod(scratch, 0755); err != nil {
		return attempt, errors.Join(err, os.RemoveAll(scratch))
	}
	m, err := NewManager(host, scratch, "default", shell, selection.Files, nil, 0, nil, WithRuntimeSelection(selection), WithServices(ports, "127.0.0.1"))
	if err != nil {
		return attempt, errors.Join(err, os.RemoveAll(scratch))
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cleanupErr := m.Close(cleanup)
		if cleanupErr == nil {
			cleanupErr = os.RemoveAll(scratch)
		}
		if cleanupErr != nil {
			fatalErr = errors.Join(fatalErr, &ProbeCleanupError{Err: cleanupErr})
		}
	}()
	networks := network.NewConfigured(ctx, selection.Network)
	networks.SetPortManager(ports)
	m.SetNetworkManager(networks)
	resources := resource.NewConfigured(cfg.Resource)
	m.SetResourceTracker(resources)
	factory, err := executor.ResolveFactory(ctx, cfg.Executor, resources)
	if err != nil {
		return attempt, err
	}
	m.SetExecutorFactory(factory)
	attempt.Executor = factory.Backend()
	if err := m.Start(ctx); err != nil {
		return attempt, err
	}
	attempt.Network = string(networks.Status().Effective)
	if err := m.ProbeEnvironment(ctx); err != nil {
		var cleanup *ProbeCleanupError
		if errors.As(err, &cleanup) {
			return attempt, err
		}
		attempt.Error = err.Error()
	}
	return attempt, nil
}
