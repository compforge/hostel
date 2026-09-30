package manager

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/bed/resource"
	"github.com/qiankunli/hostel/internal/bed/tool"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

func baselineRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		Room:       bed.Suite,
		Filesystem: isolation.Config{Bwrap: tool.Off, Landlock: tool.Off, UID: tool.Off, PRoot: tool.Off, Pathshim: tool.Off},
		Network:    network.Config{NetNS: tool.Off},
		Resource:   resource.Config{Cgroup: tool.Off},
		Executor:   executor.Config{Backend: "local"},
	}
}

func TestRuntimeBaselineExercisesCompositionAndCleansScratch(t *testing.T) {
	root := t.TempDir()
	facts := hostfacts.Collect()
	facts.EffectiveCaps = 0
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	selected, err := ResolveRuntime(ctx, facts, root, "/bin/bash", baselineRuntimeConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Executor.Backend != "local" {
		t.Fatalf("selection=%+v", selected)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("scratch allocations left behind: %v %v", entries, err)
	}
}

func TestRuntimeFallbackOnlyAfterCleanOptionalFailure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		policy    tool.Policy
		fatal     bool
		calls     int
		wantError bool
	}{
		{"optional retries", tool.Auto, false, 2, false},
		{"required retained", tool.Required, false, 1, true},
		{"cleanup stops retries", tool.Auto, true, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baselineRuntimeConfig()
			cfg.Network.NetNS = tc.policy
			facts := hostfacts.Collect()
			facts.EffectiveCaps = 0
			calls := 0
			cleanupErr := errors.New("namespace cleanup failed")
			probe := func(_ context.Context, _ hostfacts.Snapshot, _, _ string, _ RuntimeConfig, s RuntimeSelection, _ *hostnetwork.PortManager) (runtimeProbeResult, error) {
				calls++
				a := runtimeProbeResult{Filesystem: s.Files.Name(), Network: string(s.Network.Level), Executor: "local", Identity: s.Identity.Effective}
				if tc.fatal {
					return a, &ProbeCleanupError{Err: cleanupErr}
				}
				if s.Network.Level == network.Private {
					a.Error = "namespace and environment did not compose"
				}
				return a, nil
			}
			result, err := resolveRuntime(t.Context(), facts, t.TempDir(), "/bin/bash", cfg, nil, probe)
			if (err != nil) != tc.wantError || calls != tc.calls {
				t.Fatalf("calls=%d error=%v result=%+v", calls, err, result)
			}
			if tc.fatal && !errors.Is(err, cleanupErr) {
				t.Fatalf("lost cleanup error: %v", err)
			}
			if err == nil && (result.Network.Level != network.Shared || result.Network.NetNS != tool.Auto || result.Network.FallbackReason == "") {
				t.Fatalf("fallback hid policy or attempt: %+v", result)
			}
		})
	}
}

func TestRuntimeFailureDoesNotLoopOnBaseline(t *testing.T) {
	facts := hostfacts.Collect()
	facts.EffectiveCaps = 0
	calls := 0
	probe := func(_ context.Context, _ hostfacts.Snapshot, _, _ string, _ RuntimeConfig, _ RuntimeSelection, _ *hostnetwork.PortManager) (runtimeProbeResult, error) {
		calls++
		return runtimeProbeResult{Filesystem: "direct", Network: "shared", Error: "combined command failed"}, nil
	}
	_, err := resolveRuntime(t.Context(), facts, t.TempDir(), "/bin/bash", baselineRuntimeConfig(), nil, probe)
	if err == nil || !strings.Contains(err.Error(), "no executable Bed combination") {
		t.Fatalf("bad combination: %v", err)
	}
	if calls != 1 {
		t.Fatalf("repeated baseline attempt %d times", calls)
	}
}

func TestRuntimeSelectionPreservesExecutorRequirements(t *testing.T) {
	cfg := baselineRuntimeConfig()
	cfg.Executor = executor.Config{Backend: "auto", PIDNS: tool.Required}
	facts := hostfacts.Collect()
	facts.EffectiveCaps = 0
	probe := func(_ context.Context, _ hostfacts.Snapshot, _, _ string, _ RuntimeConfig, _ RuntimeSelection, _ *hostnetwork.PortManager) (runtimeProbeResult, error) {
		return runtimeProbeResult{Executor: "supervisor", Network: "shared"}, nil
	}
	selected, err := resolveRuntime(t.Context(), facts, t.TempDir(), "/bin/bash", cfg, nil, probe)
	if err != nil || selected.Executor.Backend != "supervisor" || selected.Executor.PIDNS != tool.Required {
		t.Fatalf("resolved Executor lost requirements: %+v %v", selected.Executor, err)
	}
}

func TestRuntimePIDNSFallbackPreservesPolicyAndRequiresCleanup(t *testing.T) {
	for _, policy := range []tool.Policy{tool.Auto, tool.Required} {
		for _, cleanupFailed := range []bool{false, true} {
			cfg := baselineRuntimeConfig()
			cfg.Executor.PIDNS = policy
			facts := hostfacts.Collect()
			facts.EffectiveCaps = 0
			calls := 0
			probe := func(_ context.Context, _ hostfacts.Snapshot, _, _ string, c RuntimeConfig, _ RuntimeSelection, _ *hostnetwork.PortManager) (runtimeProbeResult, error) {
				calls++
				if calls == 1 {
					a := runtimeProbeResult{Executor: "supervisor", Network: "shared", ExecutorTools: map[string]tool.Status{"pidns": tool.Describe(policy, tool.Requirements{}, true, true, true, "")}, Error: "view composition failed"}
					if cleanupFailed {
						return a, &ProbeCleanupError{Err: errors.New("cleanup pending")}
					}
					return a, nil
				}
				f, err := executor.ResolveFactory(t.Context(), c.Executor, nil)
				if err != nil {
					return runtimeProbeResult{}, err
				}
				defer f.Close()
				return runtimeProbeResult{Executor: "local", Network: "shared", ExecutorTools: f.Status().Tools}, nil
			}
			selected, err := resolveRuntime(t.Context(), facts, t.TempDir(), "/bin/bash", cfg, nil, probe)
			if policy == tool.Auto && !cleanupFailed {
				if err != nil || calls != 2 {
					t.Fatalf("auto fallback: calls=%d error=%v", calls, err)
				}
				factory, err := executor.ResolveFactory(t.Context(), selected.Executor, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer factory.Close()
				status := factory.Status().Tools["pidns"]
				if status.Policy != tool.Auto || status.Selected || status.Probe != "available" || status.Reason != "view composition failed" {
					t.Fatalf("lost evidence: %+v", status)
				}
			} else if err == nil || calls != 1 {
				t.Fatalf("required/cleanup failure retried: calls=%d error=%v", calls, err)
			}
		}
	}
}
