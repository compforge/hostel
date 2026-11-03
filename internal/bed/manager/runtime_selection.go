package manager

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"time"

	hostel "github.com/qiankunli/hostel/internal"
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
	Room         bed.RoomType
	Files        isolation.Isolator
	Identity     privilege.Selection
	Network      network.Selection
	Executor     executor.Config
	NetworkLevel network.Level
}
type runtimeProbeResult struct {
	ExecutorTools map[string]tool.Status
	Filesystem    string
	ProcessView   string
	Network       string
	Identity      privilege.Level
	Executor      string
	Err           error
	NetworkErr    error
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
	files := cfg.Filesystem
	files.Level = isolation.Private.String()
	netConfig := cfg.Network
	netConfig.Level = network.Private
	if err := errors.Join(files.Validate(), netConfig.Validate(), cfg.Resource.Validate()); err != nil {
		return RuntimeSelection{}, err
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return RuntimeSelection{}, err
	}
	identity, err := privilege.Resolve(ctx, host, cfg.Privilege, root)
	if err != nil {
		return RuntimeSelection{}, err
	}
	files.DedicatedIdentity = identity.Effective == privilege.Dedicated
	selection := RuntimeSelection{Room: cfg.Room, Identity: identity, Network: network.Selection{Config: netConfig}}
	var best *RuntimeSelection
	for {
		if err := ctx.Err(); err != nil {
			return selection, err
		}
		iso, err := resolveFiles(host, files, root)
		if err != nil {
			return selection, err
		}
		selection.Files, selection.Identity = iso, identity
		// Compare complete, verified combinations. Each file candidate gets
		// the original enhancement preferences, independently of the admission floor.
		selected, rejected, err := tryFileCombination(ctx, host, root, shell, cfg, files, selection, ports, resolveFiles, probe)
		selection = selected
		if err != nil {
			return selection, err
		}
		reason := "seeking stronger combined isolation"
		if rejected == nil {
			if best == nil || betterRuntime(selection, *best) {
				copy := selection
				best = &copy
			}
			// Lower file candidates cannot improve this combined room once
			// the strongest network has been realized for the current file level.
			if selection.NetworkLevel == network.Private {
				return *best, nil
			}
		}
		if rejected != nil {
			reason = rejected.Error()
		}
		if next, ok := isolation.NextCombination(files, iso, reason); ok {
			files = next
			continue
		}
		if best != nil {
			return *best, nil
		}
		return selection, fmt.Errorf("no executable Bed combination: %w", rejected)
	}
}

// Compare the weakest domain first. At the same overall room retain stronger
// file guarantees, then stronger network guarantees; never strip acquired features.
func betterRuntime(a, b RuntimeSelection) bool {
	ar := bed.SummarizeRoom(bed.Dorm, a.Files.Level().Room(), a.NetworkLevel.Room())
	br := bed.SummarizeRoom(bed.Dorm, b.Files.Level().Room(), b.NetworkLevel.Room())
	if ar.Effective != br.Effective {
		return bed.Rank(ar.Effective) > bed.Rank(br.Effective)
	}
	if a.Files.Level() != b.Files.Level() {
		return a.Files.Level() > b.Files.Level()
	}
	return bed.Rank(a.NetworkLevel.Room()) > bed.Rank(b.NetworkLevel.Room())
}

// tryFileCombination orders process, network and identity alternatives inside
// one file guarantee. Domain owners provide fallbacks and retain required tools;
// the coordinator only orders attempts and requires successful cleanup.
// A rejection means this file candidate was exhausted; the final error is fatal.
// Attempt details are logged, while only the final component selection is retained.
func tryFileCombination(ctx context.Context, host hostfacts.Snapshot, root, shell string, cfg RuntimeConfig, files isolation.Config, selection RuntimeSelection, ports *hostnetwork.PortManager, resolveFiles fileResolver, probe runtimeProbe) (RuntimeSelection, error, error) {
	fileLevel := selection.Files.Level()
	var best *RuntimeSelection
	for {
		candidates := cfg.Network.Candidates()
		networkIndex := 0
		netConfig := network.Selection{Config: candidates[networkIndex]}
		executorConfig := cfg.Executor
		var reason string
		var rejected error
		for {
			if err := ctx.Err(); err != nil {
				return selection, nil, err
			}
			selection.Network = netConfig
			attemptConfig := cfg
			attemptConfig.Executor = executorConfig
			attempt, fatalErr := probe(ctx, host, root, shell, attemptConfig, selection, ports)
			if fatalErr != nil {
				return selection, nil, fmt.Errorf("runtime probe: %w", fatalErr)
			}
			if attempt.Err == nil {
				selection.NetworkLevel = network.Level(attempt.Network)
				selection.Executor = executorConfig.WithSelection(attempt.ExecutorTools)
				selection.Executor.Backend = attempt.Executor
				selection.Network.Level = selection.NetworkLevel
				if selection.NetworkLevel == network.Private {
					selection.Network.Err = nil
				} else if attempt.NetworkErr != nil {
					selection.Network.Err = attempt.NetworkErr
				}
				if best == nil || betterRuntime(selection, *best) {
					copy := selection
					best = &copy
				}
				if selection.NetworkLevel == network.Private {
					return selection, nil, nil
				}
				reason = "seeking stronger network combination"
				break
			}
			rejected = attempt.Err
			reason = rejected.Error()
			selectedTools := []string{}
			for name, status := range attempt.ExecutorTools {
				if status.Selected {
					selectedTools = append(selectedTools, name)
				}
			}
			slices.Sort(selectedTools)
			log.Printf("hostel: runtime combination rejected filesystem=%s view=%s network=%s identity=%s executor=%s executor_tools=%v cause=%s reason=%q", attempt.Filesystem, attempt.ProcessView, attempt.Network, attempt.Identity, attempt.Executor, selectedTools, hostel.CauseOf(attempt.Err), attempt.Err)
			if next, ok := executorConfig.NextCombination(attempt.ExecutorTools, reason); ok {
				executorConfig = next
				continue
			}
			if networkIndex+1 < len(candidates) {
				networkIndex++
				netConfig, executorConfig = network.Selection{Config: candidates[networkIndex], Err: attempt.Err}, cfg.Executor
				continue
			}
			break
		}
		identity, ok := selection.Identity.WithoutDedicatedIdentity(reason)
		if !ok {
			if best != nil {
				return *best, nil, nil
			}
			return selection, rejected, nil
		}
		// Identity can be a prerequisite of the file boundary (UID/DAC). Re-resolve
		// it before probing, and never trade away files to obtain shared identity.
		files.DedicatedIdentity = false
		iso, err := resolveFiles(host, files, root)
		if err != nil || iso.Level() < fileLevel {
			if best != nil {
				return *best, nil, nil
			}
			return selection, rejected, nil
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
	m, err := NewManager(host, scratch, shell, selection.Files, nil, 0, nil, WithRuntimeSelection(selection), WithPillows(ports))
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
	networks := network.NewSelected(ctx, selection.Network)
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
	netStatus := networks.Status()
	attempt.Network = string(netStatus.Effective)
	attempt.NetworkErr = netStatus.Err
	if err := m.ProbeEnvironment(ctx); err != nil {
		var cleanup *ProbeCleanupError
		if errors.As(err, &cleanup) {
			return attempt, err
		}
		attempt.Err = err
	}
	return attempt, nil
}
