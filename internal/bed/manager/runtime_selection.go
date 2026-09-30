package manager

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"time"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/bed/privilege"
	"github.com/qiankunli/hostel/internal/bed/resource"
	"github.com/qiankunli/hostel/internal/bed/tool"
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
}
type runtimeProbeResult struct {
	ExecutorTools map[string]tool.Status
	Filesystem    string
	ProcessView   string
	Network       string
	Identity      privilege.Level
	Executor      string
	Error         string
}

func WithRuntimeSelection(s RuntimeSelection) ManagerOption {
	return func(m *Manager) {
		m.roomType = s.Room
		identity := s.Identity
		m.identitySelection = &identity
		m.bedUser = identity.User
	}
}

// ResolveRuntime validates candidate environments before recovering real Bed
// identities. Failed attempts own isolated scratch data and must fully release
// allocations before another attempt can run.
// +rule=`Only startup selection may retry weaker optional combinations. Required policies and cleanup errors stop selection; live Bed environments are immutable.`
func ResolveRuntime(ctx context.Context, host hostfacts.Snapshot, root, shell string, cfg RuntimeConfig, ports *hostnetwork.PortManager) (RuntimeSelection, error) {
	return resolveRuntime(ctx, host, root, shell, cfg, ports, probeRuntime)
}

type runtimeProbe func(context.Context, hostfacts.Snapshot, string, string, RuntimeConfig, RuntimeSelection, *hostnetwork.PortManager) (runtimeProbeResult, error)

func resolveRuntime(ctx context.Context, host hostfacts.Snapshot, root, shell string, cfg RuntimeConfig, ports *hostnetwork.PortManager, probe runtimeProbe) (RuntimeSelection, error) {
	return selectRuntime(ctx, host, root, shell, cfg, ports, isolation.Resolve, probe)
}

type fileResolver func(hostfacts.Snapshot, isolation.Config, string) (isolation.Isolator, error)

func selectRuntime(ctx context.Context, host hostfacts.Snapshot, root, shell string, cfg RuntimeConfig, ports *hostnetwork.PortManager, resolveFiles fileResolver, probe runtimeProbe) (RuntimeSelection, error) {
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
	selection := RuntimeSelection{Room: cfg.Room, Identity: identity, Network: netConfig}
	for {
		if err := ctx.Err(); err != nil {
			return selection, err
		}
		iso, err := resolveFiles(host, files, root)
		if err != nil {
			return selection, err
		}
		selection.Files, selection.Identity = iso, identity
		// Exhaust optional enhancements for this file boundary before excluding
		// it. Each new file candidate gets the original enhancement preferences.
		selected, reason, err := tryFileCombination(ctx, host, root, shell, cfg, files, selection, ports, resolveFiles, probe)
		selection = selected
		if err != nil || reason == "" {
			return selection, err
		}
		if next, ok := isolation.NextCombination(files, iso, reason); ok {
			files = next
			continue
		}
		return selection, fmt.Errorf("no executable Bed combination: %s", reason)
	}
}

// tryFileCombination orders process, network and identity alternatives inside
// one file guarantee. Domain owners provide fallbacks and retain required tools;
// the coordinator only orders attempts and requires successful cleanup.
// A nonempty reason means this file candidate was exhausted; errors are fatal.
// Attempt details are logged, while only the final component selection is retained.
func tryFileCombination(ctx context.Context, host hostfacts.Snapshot, root, shell string, cfg RuntimeConfig, files isolation.Config, selection RuntimeSelection, ports *hostnetwork.PortManager, resolveFiles fileResolver, probe runtimeProbe) (RuntimeSelection, string, error) {
	fileLevel := selection.Files.Level()
	for {
		netConfig := cfg.Network.ForRoom(cfg.Room)
		executorConfig := cfg.Executor
		var reason string
		for {
			if err := ctx.Err(); err != nil {
				return selection, "", err
			}
			selection.Network = netConfig
			attemptConfig := cfg
			attemptConfig.Executor = executorConfig
			attempt, fatalErr := probe(ctx, host, root, shell, attemptConfig, selection, ports)
			if fatalErr != nil {
				return selection, "", fmt.Errorf("runtime probe: %w", fatalErr)
			}
			if attempt.Error == "" {
				selection.Executor = executorConfig.WithSelection(attempt.ExecutorTools)
				selection.Executor.Backend = attempt.Executor
				if attempt.Network == string(network.Shared) && netConfig.Level == network.Private {
					if next, ok := netConfig.WithoutOptionalNamespace("private network unavailable"); ok {
						selection.Network = next
					}
				}
				return selection, "", nil
			}
			reason = attempt.Error
			selectedTools := []string{}
			for name, status := range attempt.ExecutorTools {
				if status.Selected {
					selectedTools = append(selectedTools, name)
				}
			}
			slices.Sort(selectedTools)
			log.Printf("hostel: runtime combination rejected filesystem=%s view=%s network=%s identity=%s executor=%s executor_tools=%v reason=%q", attempt.Filesystem, attempt.ProcessView, attempt.Network, attempt.Identity, attempt.Executor, selectedTools, attempt.Error)
			if next, ok := executorConfig.NextCombination(attempt.ExecutorTools, attempt.Error); ok {
				executorConfig = next
				continue
			}
			if next, ok := netConfig.WithoutOptionalNamespace(attempt.Error); ok {
				netConfig, executorConfig = next, cfg.Executor
				continue
			}
			break
		}
		identity, ok := selection.Identity.WithoutDedicatedIdentity(reason)
		if !ok {
			return selection, reason, nil
		}
		// Identity can be a prerequisite of the file boundary (UID/DAC). Re-resolve
		// it before probing, and never trade away files to obtain shared identity.
		files.DedicatedIdentity = false
		iso, err := resolveFiles(host, files, root)
		if err != nil || iso.Level() < fileLevel {
			return selection, reason, nil
		}
		selection.Identity, selection.Files = identity, iso
	}
}

func probeRuntime(ctx context.Context, host hostfacts.Snapshot, root, shell string, cfg RuntimeConfig, selection RuntimeSelection, ports *hostnetwork.PortManager) (attempt runtimeProbeResult, fatalErr error) {
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
	m, err := NewManager(host, scratch, "default", shell, selection.Files, nil, 0, nil, WithRuntimeSelection(selection), WithServices(ports))
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
	attempt.ExecutorTools = factory.Status().Tools
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
