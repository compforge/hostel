package manager

import (
	"context"
	"errors"
	"testing"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/bed/privilege"
	"github.com/qiankunli/hostel/internal/bed/tool"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

// Only selection policy is simulated here. Live boundary behavior is covered
// by TestSuiteWithSharedCapabilities through the public HTTP API.
type candidateFiles struct {
	isolation.Isolator
	isolation.Report
	name  string
	level isolation.Level
}

func (f candidateFiles) Name() string           { return f.name }
func (f candidateFiles) Level() isolation.Level { return f.level }

func TestRuntimePreservesFilesBeforeOptionalNetwork(t *testing.T) {
	for _, tc := range []struct {
		name          string
		failPrivate   bool
		networkPolicy tool.Policy
		wantFile      isolation.Level
		wantNetwork   network.Level
	}{
		{"keep private files", false, tool.Auto, isolation.Private, network.Shared},
		{"restore enhancements for next files", true, tool.Auto, isolation.Shared, network.Private},
		{"retain required network", true, tool.Required, isolation.Shared, network.Private},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baselineRuntimeConfig()
			cfg.Filesystem.Bwrap = tool.Auto
			cfg.Network.NetNS = tc.networkPolicy
			facts := hostfacts.Collect()
			facts.EffectiveCaps = 0
			base, err := isolation.Resolve(facts, baselineRuntimeConfig().Filesystem, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			resolver := func(_ hostfacts.Snapshot, c isolation.Config, _ string) (isolation.Isolator, error) {
				if c.Excluded["bwrap"] != "" {
					return base, nil
				}
				return candidateFiles{Isolator: base, Report: base.(isolation.Report), name: "bwrap", level: isolation.Private}, nil
			}
			var attempts []struct {
				files   isolation.Level
				network network.Level
			}
			probe := func(_ context.Context, _ hostfacts.Snapshot, _, _ string, _ RuntimeConfig, s RuntimeSelection, _ *hostnetwork.PortManager) (runtimeProbeResult, error) {
				attempts = append(attempts, struct {
					files   isolation.Level
					network network.Level
				}{s.Files.Level(), s.Network.Level})
				a := runtimeProbeResult{Filesystem: s.Files.Name(), Network: string(s.Network.Level), Executor: "local"}
				if s.Files.Level() == isolation.Private && (tc.failPrivate || s.Network.Level == network.Private) {
					a.Error = "incompatible environment"
				}
				return a, nil
			}
			selected, err := selectRuntime(t.Context(), facts, t.TempDir(), "/bin/bash", cfg, nil, resolver, probe)
			if err != nil {
				t.Fatal(err)
			}
			if selected.Files.Level() != tc.wantFile || selected.Network.Level != tc.wantNetwork || selected.Network.NetNS != tc.networkPolicy {
				t.Fatalf("selected=%+v files=%s attempts=%+v", selected, selected.Files.Level(), attempts)
			}
			if tc.networkPolicy == tool.Auto && (len(attempts) < 2 || attempts[1].files != isolation.Private || attempts[1].network != network.Shared) {
				t.Fatalf("file boundary sacrificed before optional network: %+v", attempts)
			}
		})
	}
}

func TestRuntimeIdentityFallbackMustPreserveFileGuarantee(t *testing.T) {
	for _, identityRequiredByFiles := range []bool{false, true} {
		cfg := baselineRuntimeConfig()
		facts := hostfacts.Collect()
		base, err := isolation.Resolve(facts, cfg.Filesystem, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		files := candidateFiles{Isolator: base, Report: base.(isolation.Report), name: "bwrap", level: isolation.Private}
		resolver := func(_ hostfacts.Snapshot, c isolation.Config, _ string) (isolation.Isolator, error) {
			if identityRequiredByFiles && !c.DedicatedIdentity {
				return base, nil
			}
			return files, nil
		}
		selection := RuntimeSelection{Room: bed.Suite, Files: files, Identity: privilege.Selection{Effective: privilege.Dedicated, User: privilege.CurrentBedUser()}}
		calls := 0
		probe := func(_ context.Context, _ hostfacts.Snapshot, _, _ string, _ RuntimeConfig, s RuntimeSelection, _ *hostnetwork.PortManager) (runtimeProbeResult, error) {
			calls++
			a := runtimeProbeResult{Executor: "local", Network: "shared"}
			if s.Identity.Effective == privilege.Dedicated {
				a.Error = "dedicated identity rejected"
			}
			return a, nil
		}
		got, reason, err := tryFileCombination(t.Context(), facts, t.TempDir(), "/bin/bash", cfg, cfg.Filesystem, selection, nil, resolver, probe)
		ok := reason == ""
		if err != nil || ok == identityRequiredByFiles {
			t.Fatalf("required=%t ok=%t error=%v", identityRequiredByFiles, ok, err)
		}
		if identityRequiredByFiles && calls != 1 {
			t.Fatal("probed a weaker file boundary")
		}
		if ok && (got.Files.Level() != isolation.Private || got.Identity.Effective != privilege.Shared) {
			t.Fatalf("selection=%+v", got)
		}
	}
}

func TestRuntimeRestoresPIDCandidateForSharedNetwork(t *testing.T) {
	cfg := baselineRuntimeConfig()
	cfg.Executor.PIDNS = tool.Auto
	cfg.Network.NetNS = tool.Auto
	facts := hostfacts.Collect()
	facts.EffectiveCaps = 0
	calls := 0
	probe := func(_ context.Context, _ hostfacts.Snapshot, _, _ string, c RuntimeConfig, s RuntimeSelection, _ *hostnetwork.PortManager) (runtimeProbeResult, error) {
		calls++
		f, err := executor.ResolveFactory(t.Context(), c.Executor, nil)
		if err != nil {
			return runtimeProbeResult{}, err
		}
		status := f.Status().Tools["pidns"]
		f.Close()
		// A fresh local candidate has not probed PIDNS; a fallback is explicitly
		// frozen with selected=false and probe=available.
		selected := status.Probe != "available"
		a := runtimeProbeResult{Executor: "local", Network: string(s.Network.Level), ExecutorTools: map[string]tool.Status{"pidns": tool.Describe(tool.Auto, tool.Requirements{}, true, true, selected, "")}}
		if s.Network.Level == network.Private {
			a.Error = "private network combination failed"
		}
		if s.Network.Level == network.Shared && !selected {
			return a, errors.New("lost original PID candidate")
		}
		return a, nil
	}
	got, err := resolveRuntime(t.Context(), facts, t.TempDir(), "/bin/bash", cfg, nil, probe)
	if err != nil || calls != 3 {
		t.Fatalf("calls=%d result=%+v error=%v", calls, got, err)
	}
}
